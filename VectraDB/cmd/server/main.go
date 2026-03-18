package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/hashicorp/raft"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rupamthxt/vectradb/internal/cluster"
	vectorGrpc "github.com/rupamthxt/vectradb/internal/grpc"
	"github.com/rupamthxt/vectradb/internal/store"
	pb "github.com/rupamthxt/vectradb/proto/pb"
	"google.golang.org/grpc"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/gofiber/fiber/v2/middleware/logger"

	vectorHttp "github.com/rupamthxt/vectradb/internal/http"
)

func main() {
	bootstrap := flag.Bool("bootstrap", false, "Bootstrap the Raft cluster (Leader only)")
	standalone := flag.Bool("standalone", true, "Standalone mode: WAL-only, no Raft consensus (fastest)")
	dim := flag.Int("dim", 128, "Vector dimension size (must match embedding model output)")
	port := flag.Int("port", 8080, "HTTP API port")
	numShardsFlag := flag.Int("shards", 3, "Number of shards")
	dataDir := flag.String("data-dir", "data", "Directory for persistent data storage")
	grpcPort := flag.Int("grpc-port", 50051, "gRPC API port (0 to disable)")
	hnswM := flag.Int("hnsw-m", 16, "HNSW M parameter: max neighbors per node")
	hnswEfMult := flag.Int("hnsw-ef-mult", 8, "HNSW efSearch multiplier: efSearch = k * this value")
	hnswEfMin := flag.Int("hnsw-ef-min", 64, "HNSW minimum efSearch value")

	flag.Parse()

	hnswCfg := store.HNSWConfig{
		M:            *hnswM,
		M0:           *hnswM * 2,
		EfSearchMult: *hnswEfMult,
		EfSearchMin:  *hnswEfMin,
		LevelMult:    1.0 / 0.69,
	}

	nodeID := "node1"
	basePort := 9000
	numShards := *numShardsFlag

	if *standalone {
		log.Printf("VectraDB standalone mode (WAL-only, no Raft)")
	} else {
		log.Printf("VectraDB Raft consensus mode")
	}

	var shards []store.ShardHandler

	for i := range numShards {
		dbPath := fmt.Sprintf("%s/%s/shard_%d/meta.bin", *dataDir, nodeID, i)
		db, err := store.NewVectraDB(*dim, dbPath, hnswCfg)
		if err != nil {
			log.Fatal(err)
		}

		if *standalone {
			shards = append(shards, &cluster.DirectNode{DB: db})
		} else {
			raftNode, err := cluster.NewRaftNode(i, nodeID, *dataDir+"/"+nodeID, basePort+i, db)
			if err != nil {
				log.Fatal(err)
			}

			if *bootstrap {
				configuration := raft.Configuration{
					Servers: []raft.Server{
						{
							ID:      raft.ServerID(fmt.Sprintf("%s-shard-%d", nodeID, i)),
							Address: raft.ServerAddress(fmt.Sprintf("127.0.0.1:%d", basePort+i)),
						},
					},
				}
				raftNode.Raft.BootstrapCluster(configuration)
			}
			shards = append(shards, raftNode)
		}
	}

	c := store.NewCluster(shards)

	app := fiber.New()
	app.Use(logger.New())

	handler := vectorHttp.NewHandler(c, *dim)
	app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))

	api := app.Group("/api/v1")
	api.Get("/info", handler.Info)
	api.Post("/insert", handler.Insert)
	api.Post("/batch_insert", handler.BatchInsert)
	api.Post("/search", handler.Search)
	api.Post("/delete", handler.Delete)

	// Initialize CollectionManager for native collections (used by gRPC)
	colManager, err := store.NewCollectionManager(*dim, *dataDir+"/"+nodeID, hnswCfg)
	if err != nil {
		log.Fatalf("Failed to init CollectionManager: %v", err)
	}

	// Start gRPC server (parallel to HTTP)
	if *grpcPort > 0 {
		go func() {
			lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *grpcPort))
			if err != nil {
				log.Fatalf("gRPC listen: %v", err)
			}
			grpcServer := grpc.NewServer()
			pb.RegisterVectraDBServiceServer(grpcServer, vectorGrpc.NewService(colManager, c, *dim))
			log.Printf("gRPC server listening on port %d", *grpcPort)
			if err := grpcServer.Serve(lis); err != nil {
				log.Fatalf("gRPC serve: %v", err)
			}
		}()
	}

	mode := "standalone/WAL"
	if !*standalone {
		mode = "Raft consensus"
	}
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("VectraDB listening on HTTP:%d gRPC:%d (dim=%d, shards=%d, mode=%s)", *port, *grpcPort, *dim, numShards, mode)

	// Graceful shutdown: flush WAL + disk on SIGTERM/SIGINT
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		log.Printf("Received %v, shutting down gracefully...", sig)

		for i, shard := range shards {
			if dn, ok := shard.(*cluster.DirectNode); ok {
				if err := dn.DB.Close(); err != nil {
					log.Printf("shard %d close error: %v", i, err)
				}
			}
		}
		if err := colManager.Close(); err != nil {
			log.Printf("collection manager close: %v", err)
		}
		log.Println("All shards flushed and closed")
		app.Shutdown()
	}()

	log.Fatal(app.Listen(addr))
}
