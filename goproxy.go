package main

import (
	"log"
	"net"
	"sync"
	"time"

	"github.com/armon/go-socks5"
)

var activeConns sync.Map

func main() {
	conf := &socks5.Config{}
	server, err := socks5.New(conf)
	if err != nil {
		log.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:1080")
	if err != nil {
		log.Fatal(err)
	}
	log.Println("[INFO] SOCKS5 proxy listening on 0.0.0.0:1080")

	// Routine per stampare le connessioni attive ogni 2 secondi
	go func() {
		for {
			time.Sleep(2 * time.Second)
			log.Println("[DEBUG] Connessioni attive:")
			count := 0
			activeConns.Range(func(key, value any) bool {
				log.Printf(" - %s", key)
				count++
				return true
			})
			if count == 0 {
				log.Println(" - Nessuna connessione attiva")
			}
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println("[ERROR] Error accepting connection:", err)
			continue
		}

		// Abilita TCP keepalive
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			tcpConn.SetKeepAlive(true)
			tcpConn.SetKeepAlivePeriod(30 * time.Second)
		}

		go func(c net.Conn) {
			addr := c.RemoteAddr().String()
			activeConns.Store(addr, true)
			log.Printf("[DEBUG] New connection from %s", addr)

			server.ServeConn(c)

			activeConns.Delete(addr)
			log.Printf("[DEBUG] Connection closed from %s", addr)
		}(conn)
	}
}
