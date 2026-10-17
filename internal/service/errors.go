package service

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/velovsky/insurance-product-service/internal/domain"
)

// ActorHeader carries the acting user or agent, e.g. "alice" or
// "agent:pricing-bot". In production it would come from a verified token.
const ActorHeader = "x-actor"

func actorFrom(ctx context.Context) (string, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if vals := md.Get(ActorHeader); len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
		return strings.TrimSpace(vals[0]), nil
	}
	return "", status.Error(codes.Unauthenticated, "missing "+ActorHeader+" metadata")
}

// toStatus maps domain errors to gRPC codes and puts validation issues into a
// BadRequest detail, so clients (and agents) get machine-readable field errors.
func toStatus(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		if _, ok := status.FromError(err); ok {
			return err
		}
		return status.Error(codes.Internal, "internal error")
	}

	code := map[domain.Kind]codes.Code{
		domain.KindInvalid:       codes.InvalidArgument,
		domain.KindNotFound:      codes.NotFound,
		domain.KindAlreadyExists: codes.AlreadyExists,
		domain.KindConflict:      codes.FailedPrecondition,
		domain.KindForbidden:     codes.PermissionDenied,
	}[de.Kind]
	if code == codes.OK {
		code = codes.Internal
	}

	st := status.New(code, de.Message)
	if len(de.Issues) == 0 {
		return st.Err()
	}
	br := &errdetails.BadRequest{}
	for _, is := range de.Issues {
		br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{Field: is.Field, Description: is.Message})
	}
	if withDetails, err := st.WithDetails(br); err == nil {
		return withDetails.Err()
	}
	return st.Err()
}
