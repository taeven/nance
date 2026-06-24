package command

import (
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// HelloReply builds the synthetic single-node primary topology response.
func HelloReply(connectionID int32, cmdName string) bson.D {
	now := time.Now().UTC()
	doc := bson.D{
		{Key: "isWritablePrimary", Value: true},
		{Key: "maxBsonObjectSize", Value: int32(16777216)},
		{Key: "maxMessageSizeBytes", Value: int32(48000000)},
		{Key: "maxWriteBatchSize", Value: int32(100000)},
		{Key: "localTime", Value: now},
		{Key: "logicalSessionTimeoutMinutes", Value: int32(30)},
		{Key: "connectionId", Value: connectionID},
		{Key: "minWireVersion", Value: int32(0)},
		{Key: "maxWireVersion", Value: int32(21)},
		{Key: "readOnly", Value: false},
		{Key: "ok", Value: float64(1)},
	}
	// isMaster clients expect ismaster / isMaster fields
	if cmdName == "isMaster" || cmdName == "ismaster" {
		doc = append(bson.D{
			{Key: "ismaster", Value: true},
			{Key: "isMaster", Value: true},
		}, doc...)
	}
	return doc
}

// BuildInfoReply is a minimal realistic buildInfo for tools/drivers.
func BuildInfoReply() bson.D {
	return bson.D{
		{Key: "version", Value: "7.0.0-nance-proxy"},
		{Key: "gitVersion", Value: "nance-accelerator-phase1"},
		{Key: "modules", Value: bson.A{}},
		{Key: "sysInfo", Value: "deprecated"},
		{Key: "versionArray", Value: bson.A{int32(7), int32(0), int32(0), int32(0)}},
		{Key: "bits", Value: int32(64)},
		{Key: "debug", Value: false},
		{Key: "maxBsonObjectSize", Value: int32(16777216)},
		{Key: "ok", Value: float64(1)},
	}
}

// OKReply is a minimal success document.
func OKReply() bson.D {
	return bson.D{{Key: "ok", Value: float64(1)}}
}

// PingReply returns ok:1.
func PingReply() bson.D {
	return OKReply()
}

// AuthOK is the successful SASL PLAIN (single-step) reply.
func AuthOK() bson.D {
	return bson.D{
		{Key: "conversationId", Value: int32(1)},
		{Key: "done", Value: true},
		{Key: "ok", Value: float64(1)},
	}
}

// AuthFailed builds code 18 Authentication failed.
func AuthFailed(msg string) bson.D {
	if msg == "" {
		msg = "Authentication failed."
	}
	return ErrorReply(18, "AuthenticationFailed", msg)
}

// NotAuthorized is returned when a command requires auth.
func NotAuthorized(msg string) bson.D {
	if msg == "" {
		msg = "command requires authentication"
	}
	return ErrorReply(13, "Unauthorized", msg)
}

// ErrorReply is the conventional MongoDB error document shape.
func ErrorReply(code int32, codeName, errmsg string) bson.D {
	return bson.D{
		{Key: "ok", Value: float64(0)},
		{Key: "errmsg", Value: errmsg},
		{Key: "code", Value: code},
		{Key: "codeName", Value: codeName},
	}
}

// MongoErrorToReply maps driver/server errors to wire error documents.
func MongoErrorToReply(err error) bson.D {
	if err == nil {
		return OKReply()
	}

	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		return bson.D{
			{Key: "ok", Value: float64(0)},
			{Key: "errmsg", Value: cmdErr.Message},
			{Key: "code", Value: cmdErr.Code},
			{Key: "codeName", Value: cmdErr.Name},
		}
	}

	var we mongo.WriteException
	if errors.As(err, &we) {
		if we.WriteConcernError != nil {
			return bson.D{
				{Key: "ok", Value: float64(0)},
				{Key: "errmsg", Value: we.WriteConcernError.Message},
				{Key: "code", Value: int32(we.WriteConcernError.Code)},
				{Key: "codeName", Value: we.WriteConcernError.Name},
			}
		}
		if len(we.WriteErrors) > 0 {
			w0 := we.WriteErrors[0]
			return bson.D{
				{Key: "n", Value: int32(0)},
				{Key: "writeErrors", Value: bson.A{
					bson.D{
						{Key: "index", Value: int32(w0.Index)},
						{Key: "code", Value: int32(w0.Code)},
						{Key: "errmsg", Value: w0.Message},
					},
				}},
				{Key: "ok", Value: float64(1)}, // write commands often still ok:1 with writeErrors
			}
		}
	}

	var sre mongo.ServerError
	if errors.As(err, &sre) {
		// HasErrorCode interface — fall through to generic
	}

	return ErrorReply(1, "InternalError", err.Error())
}

// MergeSequencesIntoInsert merges Kind 1 "documents" sequence into the insert command.
func MergeSequencesIntoInsert(cmd bson.D, sequences []struct {
	Identifier string
	Documents  []bson.Raw
}) bson.D {
	// Generic helper expects sequences from wire; implemented in handler instead.
	return cmd
}

// FormatNS builds db.collection namespace string.
func FormatNS(db, coll string) string {
	if coll == "" {
		return db
	}
	return fmt.Sprintf("%s.%s", db, coll)
}
