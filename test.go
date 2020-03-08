package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"path/filepath"

	"raft"
	"store"
	"link"
	"server"
)

func main(){
	log.SetFlags(log.LstdFlags | log.Lshortfile | log.Lmicroseconds)

	port := 8001
	if len(os.Args) > 1 {
		port, _ = strconv.Atoi(os.Args[1])
	}
	nodeId := fmt.Sprintf("%d", port)

	base_dir, _ := filepath.Abs(fmt.Sprintf("./tmp/%s", nodeId))

	/////////////////////////////////////

	log.Println("Raft server started at", port)
	store := store.OpenKVStore(base_dir + "/raft")
	raft_xport := raft.NewUdpTransport("127.0.0.1", port)
	node := raft.NewNode(nodeId, store, raft_xport)

	log.Println("Service server started at", port+1000)
	svc_xport := link.NewTcpServer("127.0.0.1", port+1000)
	svc := server.NewService(base_dir, node, svc_xport)
	defer svc.Close()

	for{
		select{
		case msg := <-svc_xport.C:
			svc.HandleClientMessage(msg)
		}
	}
}
