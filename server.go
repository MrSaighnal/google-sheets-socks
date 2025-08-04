package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

const (
	spreadsheetID = "" //PUT YOUR SHEETS ID HERE
	chunkSize     = 30240
	socksAddress  = "127.0.0.1:1080"
	role          = "server"
	nSeconds      = 1000 //SET YOUR POLLING TIME
)

var connMap sync.Map

// PUT YOUR JSON SERVICE ACCOUNT KEY HERE; SEPARATE THEM WITH COMMAS
var rawCredentials = []string{
	`
	JSON_CONTENT_FILE1
`,
	`
	JSON_CONTENT_FILE2
`,
}

type RotatingSheetsClient struct {
	clients []*sheets.Service
	mu      sync.Mutex
	index   int
}

func NewRotatingSheetsClient(rawKeys []string) (*RotatingSheetsClient, error) {
	var clients []*sheets.Service
	for i, jsonKey := range rawKeys {
		ctx := context.Background()
		srv, err := sheets.NewService(ctx, option.WithCredentialsJSON([]byte(jsonKey)))
		if err != nil {
			log.Printf("[ERROR] Key %d failed: %v", i+1, err)
			continue
		}
		clients = append(clients, srv)
	}
	if len(clients) == 0 {
		return nil, fmt.Errorf("no valid credentials found")
	}
	return &RotatingSheetsClient{clients: clients}, nil
}

func (r *RotatingSheetsClient) next() *sheets.Service {
	r.mu.Lock()
	defer r.mu.Unlock()
	client := r.clients[r.index]
	r.index = (r.index + 1) % len(r.clients)
	return client
}

func uploadChunk(r *RotatingSheetsClient, connID string, data string) error {
	const maxCellSize = 40000
	var values [][]interface{}
	for len(data) > 0 {
		chunk := data
		if len(data) > maxCellSize {
			chunk = data[:maxCellSize]
			data = data[maxCellSize:]
		} else {
			data = ""
		}
		values = append(values, []interface{}{connID, "server", time.Now().Format(time.RFC3339), chunk})
	}
	if len(values) > 0 {
		rangeData := "'Foglio1'!A:H"
		valueRange := &sheets.ValueRange{Values: values}
		var lastErr error
		for i := 0; i < len(r.clients); i++ {
			srv := r.next()
			_, err := srv.Spreadsheets.Values.Append(spreadsheetID, rangeData, valueRange).
				ValueInputOption("RAW").InsertDataOption("INSERT_ROWS").Do()
			if err == nil {
				log.Printf("[INFO] Uploaded %d chunks", len(values))
				return nil
			}
			log.Printf("[WARN] Upload failed with current key: %v", err)
			lastErr = err
		}
		return fmt.Errorf("all keys failed: %v", lastErr)
	}
	return nil
}

func downloadAndDeleteChunks(r *RotatingSheetsClient) (map[string][]string, error) {
	var lastErr error
	for i := 0; i < len(r.clients); i++ {
		srv := r.next()
		resp, err := srv.Spreadsheets.Values.Get(spreadsheetID, "'Foglio1'!A:H").Do()
		if err != nil {
			lastErr = err
			log.Printf("[WARN] Download failed with key %d: %v", i+1, err)
			continue
		}

		chunksByID := make(map[string][]string)
		var deleteRows []int

		for i, row := range resp.Values {
			if len(row) >= 4 && fmt.Sprintf("%v", row[1]) == "client" {
				connID := fmt.Sprintf("%v", row[0])
				data := fmt.Sprintf("%v", row[3])
				chunksByID[connID] = append(chunksByID[connID], data)
				deleteRows = append(deleteRows, i+1)
			}
		}

		if len(deleteRows) > 0 {
			var requests []*sheets.Request
			for _, rowIndex := range deleteRows {
				requests = append(requests, &sheets.Request{
					UpdateCells: &sheets.UpdateCellsRequest{
						Range: &sheets.GridRange{
							SheetId:          0, // Se "Foglio1" è il primo, va bene 0.
							StartRowIndex:    int64(rowIndex - 1),
							EndRowIndex:      int64(rowIndex),
							StartColumnIndex: 0,
							EndColumnIndex:   8,
						},
						Fields: "*", // Svuota tutte le celle
					},
				})
			}

			batchRequest := &sheets.BatchUpdateSpreadsheetRequest{
				Requests: requests,
			}

			_, err := srv.Spreadsheets.BatchUpdate(spreadsheetID, batchRequest).Do()
			if err != nil {
				log.Printf("Error during batch clear: %v", err)
			} else {
				log.Printf("[INFO] Cleared %d rows in batch", len(deleteRows))
			}
		}

		return chunksByID, nil
	}
	return nil, fmt.Errorf("all keys failed: %v", lastErr)
}

func getOrCreateConnection(connID string, r *RotatingSheetsClient) (net.Conn, error) {
	if conn, ok := connMap.Load(connID); ok {
		return conn.(net.Conn), nil
	}
	conn, err := net.Dial("tcp", socksAddress)
	if err != nil {
		return nil, err
	}
	connMap.Store(connID, conn)
	log.Printf("[INFO] Opened SOCKS5 connection for %s", connID)
	go handleSocksConnection(conn, r, connID)
	return conn, nil
}

func handleSocksConnection(conn net.Conn, r *RotatingSheetsClient, connID string) {
	defer conn.Close()
	buf := make([]byte, chunkSize)
	buffer := make([]byte, 0, chunkSize)
	ticker := time.NewTicker(time.Duration(nSeconds) * time.Millisecond)
	defer ticker.Stop()

	go func() {
		for range ticker.C {
			if len(buffer) > 0 {
				encoded := base64.StdEncoding.EncodeToString(buffer)
				if err := uploadChunk(r, connID, encoded); err != nil {
					log.Printf("[ERROR] Upload failed: %v", err)
				}
				buffer = buffer[:0]
			}
		}
	}()

	for {
		n, err := conn.Read(buf)
		if err != nil {
			if err == io.EOF {
				log.Printf("[INFO] Connection closed: %s", connID)
				connMap.Delete(connID)
				return
			}
			log.Printf("[ERROR] Read error: %v", err)
			continue
		}
		buffer = append(buffer, buf[:n]...)
	}
}

func main() {
	r, err := NewRotatingSheetsClient(rawCredentials)
	if err != nil {
		log.Fatalf("Error creating Sheets client: %v", err)
	}

	for {
		chunksByID, err := downloadAndDeleteChunks(r)
		if err != nil {
			log.Printf("Download error: %v", err)
			time.Sleep(time.Duration(nSeconds) * time.Millisecond)
			continue
		}
		for connID, chunks := range chunksByID {
			conn, err := getOrCreateConnection(connID, r)
			if err != nil {
				log.Printf("Connection error for %s: %v", connID, err)
				continue
			}
			for _, chunk := range chunks {
				decoded, err := base64.StdEncoding.DecodeString(chunk)
				if err != nil {
					log.Printf("Decode error: %v", err)
					continue
				}
				if _, err := conn.Write(decoded); err != nil {
					log.Printf("Write error: %v", err)
				}
			}
		}
		time.Sleep(time.Duration(nSeconds) * time.Millisecond)
	}
}
