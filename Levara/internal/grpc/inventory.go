package grpc

import (
	"sort"

	"github.com/stek0v/levara/internal/contract"
	pb "github.com/stek0v/levara/proto/pb"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// GRPCInventory walks every service exposed on :50051 and emits one
// contract.GRPCMethod per RPC, sorted by service then method.
func GRPCInventory() []contract.GRPCMethod {
	files := []protoreflect.FileDescriptor{
		pb.File_levara_proto,
	}
	if v2 := loadV2File(); v2 != nil {
		files = append(files, v2)
	}

	var out []contract.GRPCMethod
	for _, file := range files {
		services := file.Services()
		for i := 0; i < services.Len(); i++ {
			svc := services.Get(i)
			fullName := string(svc.FullName())
			methods := svc.Methods()
			for j := 0; j < methods.Len(); j++ {
				m := methods.Get(j)
				out = append(out, contract.GRPCMethod{
					Service: fullName,
					Method:  string(m.Name()),
					Status:  classifyGRPC(fullName, string(m.Name())),
				})
			}
		}
	}
	sort.Sort(contract.ByGRPCMethod(out))
	return out
}

func classifyGRPC(service, method string) contract.Status {
	if service == "levara.v2.LevaraServiceV2" {
		// v2 aliases are marked deprecated in the proto; reflect that here.
		switch method {
		case "Add", "Save", "Create":
			return contract.StatusAlias
		}
		return contract.StatusCanonical
	}
	return contract.StatusCanonical
}
