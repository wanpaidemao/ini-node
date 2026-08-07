package main

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/btcsuite/btcd/wire/v2"
)

func main() {
	host := os.Args[1]
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		fmt.Println("dial err:", err)
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(6 * time.Second))
	netv := wire.BitcoinNet(0x9d4beb9f)
	rmsg, _, err := wire.ReadMessage(conn, wire.ProtocolVersion, netv)
	if err != nil {
		fmt.Println("silent (nothing sent by peer):", err)
		return
	}
	if v, ok := rmsg.(*wire.MsgVersion); ok {
		fmt.Println("PEER SPOKE FIRST: agent=", v.UserAgent,
			" pver=", v.ProtocolVersion, " lastBlock=", v.LastBlock)
	} else {
		fmt.Printf("PEER SENT %T\n", rmsg)
	}
}
