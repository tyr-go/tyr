// Package contract is the contract of the service: its operations, with
// their requests and results, which the server implements and its clients
// call, and their documentation, which the OpenAPI and OpenRPC documents
// of the service show. The compiler checks both sides against it, the
// examples too.
//
//	links := jsonrpc.NewClient("http://shortlink.internal/rpc", &http.Client{Timeout: 5 * time.Second})
//	link, err := links.Call(ctx, contract.GetLink, contract.GetReq{Code: "go-docs"})
//
// It imports tyr and rest, for the routes of the operations, and none of
// the packages of the server.
package contract

import (
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/rest"
)

// created is the time of the links of the examples.
var created = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

// The operations of the service.
var (
	CreateLink = tyr.Define[CreateReq, Created]("links.create",
		rest.Route("POST /links"), rest.Status(http.StatusCreated),
		tyr.Summary("Create a short link"),
		tyr.Description("Without a code, the link gets a random one of 7 characters."),
		tyr.Tags("links"),
		tyr.Errors(tyr.KindAlreadyExists),
	).Example("with a code",
		CreateReq{URL: "https://go.dev/doc/", Code: "go-docs"},
		Created{Code: "go-docs", URL: "https://go.dev/doc/", CreatedAt: created, Location: "/links/go-docs"},
	)

	GetLink = tyr.Define[GetReq, Link]("links.get",
		rest.Route("GET /links/{code}"),
		tyr.Summary("Get a link"),
		tyr.Tags("links"),
		tyr.Errors(tyr.KindNotFound),
	).Example("go-docs",
		GetReq{Code: "go-docs"},
		Link{Code: "go-docs", URL: "https://go.dev/doc/", CreatedAt: created},
	)

	FollowLink = tyr.Define[GetReq, FollowRes]("links.follow",
		rest.Route("GET /{code}"), rest.Status(http.StatusFound),
		tyr.Summary("Follow a link"),
		tyr.Description("Over REST, the short link itself: a redirect to where it leads. Over JSON-RPC, where it leads."),
		tyr.Tags("links"),
		tyr.Errors(tyr.KindNotFound),
	).Example("go-docs", GetReq{Code: "go-docs"}, FollowRes{URL: "https://go.dev/doc/"})

	DeleteLink = tyr.Define[DeleteReq, struct{}]("links.delete",
		rest.Route("DELETE /links/{code}"),
		tyr.Summary("Delete a link"),
		tyr.Tags("links"),
		tyr.Errors(tyr.KindNotFound),
	)

	PurgeLinks = tyr.Define[PurgeReq, PurgeRes]("links.purge", // no REST route: JSON-RPC only
		tyr.Summary("Delete the links to a host"),
		tyr.Description("For a host that serves malware, say."),
		tyr.Tags("links"),
	).Example("example.com", PurgeReq{Host: "example.com"}, PurgeRes{Purged: 2})
)

// Link is a short link.
type Link struct {
	Code      string    `json:"code" doc:"The code of the link: the path of the short link."`
	URL       string    `json:"url" doc:"Where the link leads."`
	CreatedAt time.Time `json:"created_at" doc:"When the link was created."`
}

// Created is a link just created, with the path of its resource, which
// REST sends as the Location of 201 Created. JSON-RPC sends only the link.
type Created struct {
	Link
	Location string `json:"-" header:"Location" doc:"The path of the link."`
}

// CreateReq is a request to create a link. Without a code, the link gets a
// random one.
type CreateReq struct {
	URL  string `json:"url" validate:"required,http_url" doc:"Where the link leads: an http or https URL."`
	Code string `json:"code" validate:"omitempty,min=4,max=16" doc:"The code of the link, of a-z, 0-9 and '-', other than livez and readyz, the paths of the probes of the service. Without it, the link gets a random one."`
}

var codeRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// reserved are the codes whose paths the service takes for its probes: the
// short link /readyz couldn't be followed.
var reserved = []string{"livez", "readyz"}

// Validate holds the rules tags can't express: the characters of the code,
// and the codes the service takes.
func (r CreateReq) Validate() error {
	var v tyr.Violations
	switch {
	case r.Code != "" && !codeRe.MatchString(r.Code):
		v.Add("code", "only a-z, 0-9 and '-'")
	case slices.Contains(reserved, r.Code):
		v.Add("code", "is taken by the service")
	}
	return v.Err()
}

// GetReq is a request for a link.
type GetReq struct {
	Code string `json:"code" path:"code" validate:"required" doc:"The code of the link."`
}

// FollowRes is where a link leads: REST redirects to it, JSON-RPC returns
// it.
type FollowRes struct {
	URL string `json:"url" header:"Location" doc:"Where the link leads."`
}

// DeleteReq is a request to delete a link.
type DeleteReq struct {
	Code string `json:"code" path:"code" validate:"required" doc:"The code of the link."`
}

// PurgeReq is a request to delete the links to a host, such as one that
// serves malware.
type PurgeReq struct {
	Host string `json:"host" validate:"required,max=253" doc:"The host whose links to delete, such as example.com."`
}

// PurgeRes is the result of a purge.
type PurgeRes struct {
	Purged int `json:"purged" doc:"The number of links deleted."`
}
