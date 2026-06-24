package command

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestClassifyFind(t *testing.T) {
	raw, _ := bson.Marshal(bson.D{
		{Key: "find", Value: "users"},
		{Key: "$db", Value: "mydb"},
		{Key: "filter", Value: bson.D{{Key: "a", Value: 1}}},
	})
	info, err := Classify(raw)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "find" || info.DB != "mydb" || info.Collection != "users" || info.Kind != KindRead {
		t.Fatalf("%+v", info)
	}
}

func TestIsPreAuthAllowed(t *testing.T) {
	if !IsPreAuthAllowed("hello") || !IsPreAuthAllowed("saslStart") || IsPreAuthAllowed("find") {
		t.Fatal("pre-auth gate wrong")
	}
}
