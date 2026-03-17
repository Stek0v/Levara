package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/hashicorp/raft"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rupamthxt/vectradb/internal/cluster"
	"github.com/rupamthxt/vectradb/internal/store"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/gofiber/fiber/v2/middleware/logger"

	vectorHttp "github.com/rupamthxt/vectradb/internal/http"
)

const SnapshotPath = "./vectradb.snap"

func main() {
	fmt.Println("Initializing VectraDB (High-Perf) mode...")

	bootstrap := flag.Bool("bootstrap", false, "Bootstrap the cluster (Leader only)")
	dim := flag.Int("dim", 128, "Vector dimension size (must match embedding model output)")
	port := flag.Int("port", 8080, "HTTP API port")
	numShardsFlag := flag.Int("shards", 3, "Number of shards")
	dataDir := flag.String("data-dir", "data", "Directory for persistent data storage")

	flag.Parse()

	nodeID := "node1"
	basePort := 9000
	numShards := *numShardsFlag

	var shards []store.ShardHandler

	for i := range numShards {
		dbPath := fmt.Sprintf("%s/%s/shard_%d/meta.bin", *dataDir, nodeID, i)
		db, err := store.NewVectraDB(*dim, dbPath)
		if err != nil {
			log.Fatal(err)
		}

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

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("VectraDB listening on port %d (dim=%d, shards=%d)", *port, *dim, numShards)
	log.Fatal(app.Listen(addr))
}
