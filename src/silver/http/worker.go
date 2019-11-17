package http

import (
	"log"
	"net/http"
	"silver/cluster"
	"silver/storage"
)

type Server struct {
	storage.Storage
	cluster.Node
}

func (s *Server) Listen(){
	http.Handle("/cache",s.cacheHandler())
	http.Handle("/status",s.statusHandler())
	http.Handle("/cluster",s.clusterHandler())
	err:=http.ListenAndServe(":12345",nil)
	if err!=nil{
	  log.Println(err.Error())
	}
}

func New(c storage.Storage,n cluster.Node) *Server{
	return &Server{c,n}
}
