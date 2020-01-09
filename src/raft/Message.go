package raft

import (
	"fmt"
	"strings"
	"strconv"
)

type Message struct{
	Cmd string
	Src string
	Dst string
	Index uint64
	Term uint32
	Data string
}

func (m *Message)Encode() []byte{
	return EncodeMessage(m)
}

func atou(s string) uint64{
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

func utoa(u uint64) string{
	return fmt.Sprintf("%d", u)
}

func EncodeMessage(msg *Message) []byte{
	ps := []string{msg.Cmd, msg.Src, msg.Dst, utoa(msg.Index), utoa(uint64(msg.Term)), msg.Data}
	return []byte(strings.Join(ps, " "))
}

func DecodeMessage(buf []byte) *Message{
	s := string(buf)
	s = strings.Trim(s, "\r\n")
	ps := strings.SplitN(s, " ", 6)
	if len(ps) != 6 {
		return nil
	}
	msg := new(Message);
	msg.Cmd = ps[0]
	msg.Src = ps[1]
	msg.Dst = ps[2]
	msg.Index = atou(ps[3])
	msg.Term = uint32(atou(ps[4]))
	msg.Data = ps[5]
	return msg
}
