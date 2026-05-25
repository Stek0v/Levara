package grpc

import (
	"google.golang.org/protobuf/reflect/protoreflect"

	pbv2 "github.com/stek0v/levara/proto/pb/v2"
)

func loadV2File() protoreflect.FileDescriptor {
	return pbv2.File_levara_v2_proto
}
