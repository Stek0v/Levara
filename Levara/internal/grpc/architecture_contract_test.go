package grpc

import (
	"testing"

	pb "github.com/stek0v/levara/proto/pb"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestGRPCServiceArchitectureContract(t *testing.T) {
	service := pb.File_levara_proto.Services().ByName(protoreflect.Name("LevaraService"))
	if service == nil {
		t.Fatal("LevaraService descriptor missing")
	}
	methods := map[string]bool{}
	for i := 0; i < service.Methods().Len(); i++ {
		methods[string(service.Methods().Get(i).Name())] = true
	}

	for _, critical := range []string{
		"CreateCollection",
		"BatchInsert",
		"Search",
		"GetByID",
		"SearchByText",
		"PipelineCognify",
		"BM25Search",
		"HybridSearch",
		"GraphRead",
		"GraphCompletionSearch",
		"TemporalSearch",
	} {
		if !methods[critical] {
			t.Fatalf("critical gRPC method missing from proto descriptor: %s", critical)
		}
	}
}
