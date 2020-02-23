package main

import (
	"fmt"
	"time"
	"log"
	// "math/rand"
	"os"
	"strconv"
	"strings"

	"raft"
	"ssdb"
	"link"
)

type Service struct{
	lastApplied int64
	
	db *ssdb.Db

	node *raft.Node
	xport *link.TcpServer
	
	jobs map[int64]*link.Message // raft.Index => msg
}

func NewService(dir string, node *raft.Node, xport *link.TcpServer) *Service {
	svc := new(Service)
	
	svc.db = ssdb.OpenDb(dir + "/data")
	svc.lastApplied = svc.db.CommitIndex()

	svc.node = node
	svc.node.AddService(svc)
	
	svc.xport = xport

	svc.jobs = make(map[int64]*link.Message)

	return svc
}

func (svc *Service)HandleClientMessage(req *link.Message) {
	ps := strings.Split(req.Data, " ")

	cmd := ps[0]
	var key string
	var val string
	if len(ps) > 1 {
		key = ps[1]
	}
	if len(ps) > 2 {
		val = ps[2]
	}

	if cmd == "JoinGroup" {
		svc.node.JoinGroup(ps[1], ps[2])
		return
	}
	if cmd == "AddMember" {
		if svc.node.Role == "leader" {
			svc.node.AddMember(ps[1], ps[2])
			return
		}
	}
	
	if svc.node.Role != "leader" {
		log.Println("error: not leader")
		resp := &link.Message{req.Src, "error: not leader"}
		svc.xport.Send(resp)
		return
	}
	
	if cmd == "get" {
		s := svc.db.Get(key)
		log.Println(key, "=", s)
		resp := &link.Message{req.Src, s}
		svc.xport.Send(resp)
		return
	}

	var s string
	if cmd == "set" {
		s = fmt.Sprintf("%s %s %s", cmd, key, val)
	}else if cmd == "incr" {
		if val == "" {
			val = "1"
		}
		s = fmt.Sprintf("%s %s %s", cmd, key, val)
	}else if cmd == "del" {
		s = fmt.Sprintf("%s %s", cmd, key)
	}
	
	if s == "" {
		log.Println("error: unknown cmd: " + cmd)
		resp := &link.Message{req.Src, "error: unkown cmd " + cmd}
		svc.xport.Send(resp)
		return
	}
	
	idx := svc.node.Write(s)
	svc.jobs[idx] = req
}

func (svc *Service)raftApplyCallback(ent *raft.Entry, ret string) {
	req := svc.jobs[ent.Index]
	if req == nil {
		return
	}
	delete(svc.jobs, ent.Index)
	
	if ret == "" {
		ret = "ok"
	}
	resp := &link.Message{req.Src, ret}
	svc.xport.Send(resp)
}

/* #################### raft.Service interface ######################### */

func (svc *Service)LastApplied() int64{
	return svc.lastApplied
}

func (svc *Service)ApplyEntry(ent *raft.Entry){
	// 不需要持久化, 从 Redolog 中获取
	svc.lastApplied = ent.Index

	if ent.Type == "Write"{
		log.Println("[Apply]", ent.Data)
		var ret string

		ps := strings.SplitN(ent.Data, " ", 3)
		cmd := ps[0]
		key := ps[1]
		if cmd == "set" {
			val := ps[2]
			svc.db.Set(ent.Index, key, val)
		} else if cmd == "del" {
			svc.db.Del(ent.Index, key)
		} else if cmd == "incr" {
			val := ps[2]
			ret = svc.db.Incr(ent.Index, key, val)
		}
	
		svc.raftApplyCallback(ent, ret)
	}
}

/* ############################################# */

const TimerInterval = 100

func main(){
	log.SetFlags(log.LstdFlags | log.Lshortfile | log.Lmicroseconds)

	port := 8001
	if len(os.Args) > 1 {
		port, _ = strconv.Atoi(os.Args[1])
	}
	nodeId := fmt.Sprintf("%d", port)

	base_dir := fmt.Sprintf("./tmp/%s", nodeId);

	/////////////////////////////////////

	ticker := time.NewTicker(TimerInterval * time.Millisecond)
	defer ticker.Stop()

	/////////////////////////////////////

	log.Println("Raft server started at", port)
	store := raft.OpenStorage(base_dir + "/raft")
	raft_xport := raft.NewUdpTransport("127.0.0.1", port)

	node := raft.NewNode(nodeId, store, raft_xport)
	node.Start()

	log.Println("Service server started at", port+1000)
	svc_xport := link.NewTcpServer("127.0.0.1", port+1000)
	
	svc := NewService(base_dir, node, svc_xport)

	for{
		select{
		case <-ticker.C:
			node.Tick(TimerInterval)
		case msg := <-raft_xport.C:
			node.HandleRaftMessage(msg)
		case msg := <-svc_xport.C:
			svc.HandleClientMessage(msg)
		}
	}
}
