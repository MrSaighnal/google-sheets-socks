// Nuovo CLIENT con rotazione chiavi (hardcoded inline)
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
	role          = "client"
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
		b := []byte(jsonKey)
		srv, err := sheets.NewService(ctx, option.WithCredentialsJSON(b))
		if err != nil {
			log.Printf("[ERROR] Cannot create Sheets client #%d: %v", i+1, err)
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

func uploadBatchChunks(r *RotatingSheetsClient, values [][]interface{}) error {
	rangeData := "'Foglio1'!A:H"
	valueRange := &sheets.ValueRange{Values: values}
	var lastErr error
	for i := 0; i < len(r.clients); i++ {
		srv := r.next()
		_, err := srv.Spreadsheets.Values.Append(spreadsheetID, rangeData, valueRange).
			ValueInputOption("RAW").InsertDataOption("INSERT_ROWS").Do()
		if err == nil {
			log.Println("[INFO] Batch uploaded to Google Sheets")
			return nil
		}
		log.Printf("[WARN] Upload failed, rotating: %v", err)
		lastErr = err
	}
	return fmt.Errorf("all keys failed: %v", lastErr)
}

func downloadAndDeleteChunks(r *RotatingSheetsClient, role string) ([]string, []string, error) {
	var lastErr error
	for i := 0; i < len(r.clients); i++ {
		srv := r.next()
		resp, err := srv.Spreadsheets.Values.Get(spreadsheetID, "'Foglio1'!A:H").Do()
		if err != nil {
			log.Printf("[WARN] Download failed, rotating: %v", err)
			lastErr = err
			continue
		}

		connIDs := []string{}
		chunks := []string{}
		deleteRows := []int{}

		for i, row := range resp.Values {
			if len(row) >= 4 {
				connID := fmt.Sprintf("%v", row[0])
				if len(connID) > 0 && connID[0] == '\'' {
					connID = connID[1:]
				}
				source := fmt.Sprintf("%v", row[1])
				data := fmt.Sprintf("%v", row[3])

				if (role == "client" && source == "server") || (role == "server" && source == "client") {
					connIDs = append(connIDs, connID)
					chunks = append(chunks, data)
					deleteRows = append(deleteRows, i+1)
				}
			}
		}

		if len(deleteRows) > 0 {
			var requests []*sheets.Request
			for _, rowIndex := range deleteRows {
				requests = append(requests, &sheets.Request{
					UpdateCells: &sheets.UpdateCellsRequest{
						Range: &sheets.GridRange{
							SheetId:          0, // Assumiamo il primo foglio. Se vuoi essere più preciso, possiamo ottenere l’ID dinamicamente.
							StartRowIndex:    int64(rowIndex - 1),
							EndRowIndex:      int64(rowIndex),
							StartColumnIndex: 0,
							EndColumnIndex:   8, // Colonne A:H => 8 colonne
						},
						Fields: "*", // Cancella tutto
					},
				})
			}

			batchRequest := &sheets.BatchUpdateSpreadsheetRequest{
				Requests: requests,
			}

			_, err := srv.Spreadsheets.BatchUpdate(spreadsheetID, batchRequest).Do()
			if err != nil {
				log.Printf("Error batch clearing rows: %v", err)
			} else {
				log.Printf("[INFO] Cleared %d rows in batch", len(deleteRows))
			}
		}

		return connIDs, chunks, nil
	}
	return nil, nil, fmt.Errorf("all keys failed: %v", lastErr)
}

func handleConnection(conn net.Conn, r *RotatingSheetsClient, connID string) {
	defer conn.Close()
	buffer := make([]byte, chunkSize)
	var batch [][]interface{}
	ticker := time.NewTicker(time.Duration(nSeconds) * time.Millisecond)
	defer ticker.Stop()

	go func() {
		for range ticker.C {
			if len(batch) > 0 {
				if err := uploadBatchChunks(r, batch); err != nil {
					log.Printf("Error uploading batch: %v", err)
				} else {
					log.Printf("[INFO] Uploaded batch of %d rows", len(batch))
				}
				batch = [][]interface{}{}
			}
		}
	}()

	for {
		n, err := conn.Read(buffer)
		if err != nil {
			if err == io.EOF {
				log.Println("Connection closed by client")
				connMap.Delete(connID)
				break
			}
			log.Printf("Error reading: %v", err)
			continue
		}
		encoded := base64.StdEncoding.EncodeToString(buffer[:n])
		row := []interface{}{"'" + connID, "client", time.Now().Format(time.RFC3339), encoded}
		batch = append(batch, row)
		log.Printf("Accumulated chunk of length %d", n)
	}
}

func main() {
	r, err := NewRotatingSheetsClient(rawCredentials)
	if err != nil {
		log.Fatalf("Error creating rotating Sheets client: %v", err)
	}

	listener, err := net.Listen("tcp", ":9191")
	if err != nil {
		log.Fatalf("Error starting listener: %v", err)
	}
	defer listener.Close()
	log.Println("Listening on port 9191")

	go func() {
		for {
			connIDs, chunks, err := downloadAndDeleteChunks(r, role)
			if err != nil {
				log.Printf("Error downloading chunks: %v", err)
				time.Sleep(time.Duration(nSeconds) * time.Millisecond)
				continue
			}
			for i, chunk := range chunks {
				if conn, ok := connMap.Load(connIDs[i]); ok {
					decoded, err := base64.StdEncoding.DecodeString(chunk)
					if err != nil {
						log.Printf("Error decoding chunk: %v", err)
						continue
					}
					_, err = conn.(net.Conn).Write(decoded)
					if err != nil {
						log.Printf("Error sending data to connection: %v", err)
					}
				}
			}
			time.Sleep(time.Duration(nSeconds) * time.Millisecond)
		}
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Error accepting connection: %v", err)
			continue
		}
		connID := fmt.Sprintf("%d", time.Now().UnixNano())
		connMap.Store(connID, conn)
		log.Printf("Accepted new connection with ID %s", connID)
		go handleConnection(conn, r, connID)
	}
}
