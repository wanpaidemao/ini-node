package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	host := os.Args[1]
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		fmt.Println("dial err:", err)
		return
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		fmt.Printf("SILENT-CONNECT read -> n=%d err=%#v (%T)\n", n, err, err)
		return
	}
	fmt.Printf("SILENT-CONNECT got %d bytes without sending anything: %x\n", n, buf[:n])
}
