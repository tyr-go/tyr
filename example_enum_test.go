package tyr_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/rest"
)

// Status is the status of a task: an enum, whose values the API checks and
// the documents list.
type Status string

const (
	StatusTodo  Status = "todo"
	StatusDoing Status = "doing"
	StatusDone  Status = "done"
)

// EnumValues lists the statuses, as tyr.Enum has it.
func (Status) EnumValues() []Status { return []Status{StatusTodo, StatusDoing, StatusDone} }

type SetStatusReq struct {
	ID     string `json:"id" path:"id"`
	Status Status `json:"status" validate:"required"`
}

type Task struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
}

func ExampleEnum() {
	api := tyr.New()
	api.Handle("tasks.set_status", func(ctx context.Context, req SetStatusReq) (Task, error) {
		return Task{ID: req.ID, Title: "Draw a logo", Status: req.Status}, nil
	}, rest.Route("PUT /tasks/{id}/status"))

	mux := http.NewServeMux()
	routes := rest.Mount(mux, api)

	for _, body := range []string{`{"status":"done"}`, `{"status":"later"}`} {
		req := httptest.NewRequest("PUT", "/tasks/42/status", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		fmt.Println(rec.Code, rec.Body)
	}

	// The schema of the statuses, one for requests and results.
	var doc struct {
		Components struct {
			Schemas map[string]jsontext.Value `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(routes.OpenAPIJSON(tyr.Info{Title: "tasks", Version: "1.0.0"}), &doc); err != nil {
		fmt.Println(err)
	}
	status := doc.Components.Schemas["Status"]
	_ = status.Compact()
	fmt.Println(status)
	// Output:
	// 200 {"id":"42","title":"Draw a logo","status":"done"}
	// 400 {"type":"/problems/invalid_argument","title":"Invalid Argument","status":400,"detail":"validation failed","kind":"invalid_argument","errors":[{"pointer":"/status","detail":"must be one of: todo, doing, done"}]}
	// {"type":"string","enum":["todo","doing","done"]}
}
