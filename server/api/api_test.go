package api_test

import (
	"context"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
)

func TestSpecValidates(t *testing.T) {
	doc, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi = %q", doc.OpenAPI)
	}
}

func TestEveryOperationHasAnID(t *testing.T) {
	doc, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.OperationID == "" {
				t.Errorf("%s %s has no operationId", method, path)
			}
		}
	}
}
