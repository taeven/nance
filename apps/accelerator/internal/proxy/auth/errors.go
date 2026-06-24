package auth

import "go.mongodb.org/mongo-driver/bson"

// TooManyConnectionsDoc is returned when a tenant exceeds MaxConnsPerTenant.
func TooManyConnectionsDoc() bson.D {
	return bson.D{
		{Key: "ok", Value: float64(0)},
		{Key: "errmsg", Value: "too many connections for tenant"},
		{Key: "code", Value: int32(9001)},
		{Key: "codeName", Value: "TooManyConnections"},
	}
}
