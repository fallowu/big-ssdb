package raft

import (
	"fmt"
	"net"
	"log"
	"strings"
)

type UdpTransport struct{
	addr string
	C chan []byte
	conn *net.UDPConn
	dns map[string]string
}

func NewUdpTransport(ip string, port int) (*UdpTransport){
	s := fmt.Sprintf("%s:%d", ip, port)
	addr, _ := net.ResolveUDPAddr("udp", s)
	conn, _ := net.ListenUDP("udp", addr)

	tp := new(UdpTransport)
	tp.addr = fmt.Sprintf("%s:%d", ip, port)
	tp.conn = conn
	tp.C = make(chan []byte)
	tp.dns = make(map[string]string)

	tp.start()
	return tp
}

func (tp *UdpTransport)Addr() string {
	return tp.addr
}

func (tp *UdpTransport)start(){
	go func(){
		buf := make([]byte, 1024 * 64)
		for{
			n, _, _ := tp.conn.ReadFromUDP(buf)
			data := make([]byte, n)
			copy(data, buf[:n])
			log.Printf("    receive < %s\n", strings.Trim(string(data), "\r\n"))
			tp.C <- data
		}
	}()
}

func (tp *UdpTransport)Close(){
	tp.conn.Close()
	close(tp.C)
} 

func (tp *UdpTransport)Connect(nodeId, addr string){
	tp.dns[nodeId] = addr
}

func (tp *UdpTransport)Disconnect(nodeId string){
	delete(tp.dns, nodeId)
}

func (tp *UdpTransport)Send(msg *Message) bool{
	addr := tp.dns[msg.Dst]
	if addr == "" {
		log.Printf("dst: %s not connected", msg.Dst)
		return false
	}

	buf := []byte(msg.Encode())
	uaddr, _ := net.ResolveUDPAddr("udp", addr)
	n, _ := tp.conn.WriteToUDP(buf, uaddr)
	log.Printf("    send > %s\n", strings.Trim(string(buf), "\r\n"))
	return n > 0
}
