package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stek0v/levara/pkg/graphdb"
	pb "github.com/stek0v/levara/proto/pb"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestGraphBatchPropertiesJSON(t *testing.T) {
	for _, input := range []string{"", "null", " \n null \t", "{}"} {
		props, err := parseGraphBatchProperties(input, true)
		if err != nil || len(props) != 0 {
			t.Fatalf("%q: %v %v", input, props, err)
		}
	}
	for _, input := range []string{" ", "{", "[]", "1", "true", "\"text\"", "{} {}", "null false", "{\"x\":NaN}"} {
		if _, err := parseGraphBatchProperties(input, true); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	for _, number := range []string{"9007199254740993.0", "9007199254740992.1", "1e-400", "1e400"} {
		props, err := parseGraphBatchProperties(fmt.Sprintf("{\"valid_from\":%s,\"valid_until\":%s,\"weight\":1.5,\"nested\":{\"count\":2},\"values\":[3]}", number, number), true)
		if err != nil {
			t.Fatal(err)
		}
		if props["valid_from"] != json.Number(number) || props["valid_until"] != json.Number(number) {
			t.Fatalf("rounded: %#v", props)
		}
		if props["weight"] != float64(1.5) || !reflect.DeepEqual(props["nested"], map[string]any{"count": float64(2)}) || !reflect.DeepEqual(props["values"], []any{float64(3)}) {
			t.Fatalf("ordinary decoding changed: %#v", props)
		}
	}
	props, err := parseGraphBatchProperties("{\"valid_from\":\"2020-01-01T00:00:00Z\",\"valid_until\":null}", true)
	if err != nil || props["valid_from"] != "2020-01-01T00:00:00Z" || props["valid_until"] != nil {
		t.Fatalf("string/null changed: %v %v", props, err)
	}
	nodeProps, nodeErr := parseGraphBatchProperties("{\"valid_from\":12.5,\"valid_until\":13}", false)
	if nodeErr != nil || nodeProps["valid_from"] != float64(12.5) || nodeProps["valid_until"] != float64(13) {
		t.Fatalf("node ordinary bounds changed: %#v %v", nodeProps, nodeErr)
	}
}

func TestGraphBatchInvalidBeforeConnection(t *testing.T) {
	endpoint := "bolt://127.0.0.1:1"
	t.Setenv("NEO4J_URL_ALLOWLIST", endpoint)
	service := &Service{}
	cases := []*pb.BatchWriteGraphReq{nil, {Neo4JUrl: endpoint, Nodes: []*pb.GraphNodeWrite{nil}}, {Neo4JUrl: endpoint, Edges: []*pb.GraphEdgeWrite{nil}}}
	for _, bad := range []string{"{", "[]", "true", "{} {}"} {
		cases = append(cases, &pb.BatchWriteGraphReq{Neo4JUrl: endpoint, Nodes: []*pb.GraphNodeWrite{{Id: "first", Label: "Entity", PropertiesJson: "{}"}, {PropertiesJson: bad}}}, &pb.BatchWriteGraphReq{Neo4JUrl: endpoint, Nodes: []*pb.GraphNodeWrite{{Id: "first", Label: "Entity"}}, Edges: []*pb.GraphEdgeWrite{{PropertiesJson: bad}}})
	}
	for i, req := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		resp, err := service.BatchWriteGraph(ctx, req)
		cancel()
		if status.Code(err) != codes.InvalidArgument || resp != nil {
			t.Fatalf("case %d: response %v, error %v", i, resp, err)
		}
	}
	if err := validateNeo4jURL("bolt://127.0.0.1:2"); err == nil {
		t.Fatal("unapproved endpoint accepted")
	}
}

// This opt-in fixture never deletes the shared graph: cleanup touches only its
// UUID-namespaced nodes and episode reservations on the approved loopback server.
func TestGraphBatchJSONLiveClient(t *testing.T) {
	endpoint := os.Getenv("NEO4J_TEST_URL")
	if endpoint == "" {
		t.Skip("NEO4J_TEST_URL required (owned private Neo4j/APOC)")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Fatal("live fixture requires approved loopback Neo4j")
	}
	t.Setenv("NEO4J_URL_ALLOWLIST", endpoint)
	user, password, database := os.Getenv("NEO4J_TEST_USER"), os.Getenv("NEO4J_TEST_PASSWORD"), os.Getenv("NEO4J_TEST_DATABASE")
	if user == "" {
		user = "neo4j"
	}
	if password == "" {
		password = "test"
	}
	if database == "" {
		database = "neo4j"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	writer, err := graphdb.NewWriter(ctx, endpoint, user, password, database)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(context.Background())
	prefix := "grpc-json-" + uuid.NewString() + "-"
	var nodeIDs, edgeIDs []string
	defer func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanCancel()
		_, err := graphdb.RunWriteTx(cleanCtx, writer, func(ctx context.Context, tx neo4j.ManagedTransaction) (bool, error) {
			for _, item := range []struct {
				query string
				ids   []string
			}{
				{"MATCH (n:__Node__) WHERE n.id IN $ids DETACH DELETE n", nodeIDs},
				{"MATCH (n:__EpisodeIdentity__) WHERE n.id IN $ids DELETE n", edgeIDs},
			} {
				result, err := tx.Run(ctx, item.query, map[string]any{"ids": item.ids})
				if err != nil {
					return false, err
				}
				if _, err := result.Consume(ctx); err != nil {
					return false, err
				}
			}
			return true, nil
		})
		if err != nil {
			t.Errorf("targeted fixture cleanup: %v", err)
		}
	}()
	listener := bufconn.Listen(1024 * 1024)
	defer listener.Close()
	server := grpclib.NewServer()
	pb.RegisterLevaraServiceServer(server, &Service{})
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpclib.NewClient("passthrough:///graph-json", grpclib.WithTransportCredentials(insecure.NewCredentials()), grpclib.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pb.NewLevaraServiceClient(conn)
	for i, number := range []string{"9007199254740993.0", "9007199254740992.1", "1e-400"} {
		source, target, edgeID := fmt.Sprintf("%ss%d", prefix, i), fmt.Sprintf("%st%d", prefix, i), fmt.Sprintf("%se%d", prefix, i)
		nodeIDs = append(nodeIDs, source, target)
		edgeIDs = append(edgeIDs, edgeID)
		req := &pb.BatchWriteGraphReq{Neo4JUrl: endpoint, Neo4JUser: user, Neo4JPassword: password, Neo4JDatabase: database,
			Nodes: []*pb.GraphNodeWrite{{Id: source, Label: "Entity", PropertiesJson: "{\"valid_from\":12.5,\"valid_until\":13}"}, {Id: target, Label: "Entity", PropertiesJson: ""}},
			Edges: []*pb.GraphEdgeWrite{{SourceId: source, TargetId: target, RelationshipName: "knows", PropertiesJson: fmt.Sprintf("{\"id\":%q,\"valid_from\":%s,\"valid_until\":%s,\"weight\":1.5,\"nested\":{\"count\":2}}", edgeID, number, number)}},
		}
		resp, err := client.BatchWriteGraph(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := writer.Query(ctx, "MATCH (a:__Node__ {id:$source})-[r]->() RETURN properties(r) AS props", map[string]any{"source": source})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if len(resp.Errors) != 0 || resp.NodesWritten != 2 || resp.EdgesWritten != 1 || len(rows) != 1 {
				t.Fatalf("exact write: %v rows %v", resp, rows)
			}
			nodeRows, nodeErr := writer.Query(ctx, "MATCH (n:__Node__ {id:$id}) RETURN n.valid_from AS start, n.valid_until AS end", map[string]any{"id": source})
			if nodeErr != nil || len(nodeRows) != 1 || nodeRows[0]["start"] != float64(12.5) || nodeRows[0]["end"] != float64(13) {
				t.Fatalf("node ordinary bound compatibility: %#v %v", nodeRows, nodeErr)
			}
			props := rows[0]["props"].(map[string]any)
			if props["valid_from"] != int64(9007199254740993) || props["valid_until"] != int64(9007199254740993) || props["weight"] != float64(1.5) || props["nested_json"] != "{\"count\":2}" {
				t.Fatalf("stored precision/ordinary evidence: %#v", props)
			}
		} else {
			if len(resp.Errors) == 0 || resp.NodesWritten != 0 || resp.EdgesWritten != 0 || len(rows) != 0 {
				t.Fatalf("invalid bound effects: %v rows %v", resp, rows)
			}
			counts, err := writer.Query(ctx, "MATCH (n) WHERE n.id IN $ids RETURN count(n) AS count", map[string]any{"ids": []string{source, target, edgeID}})
			if err != nil {
				t.Fatal(err)
			}
			if counts[0]["count"] != int64(0) {
				t.Fatalf("invalid batch left nodes/reservations: %v", counts)
			}
		}
	}
}
