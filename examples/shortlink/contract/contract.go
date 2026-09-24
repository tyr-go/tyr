// Package contract is the contract of the service: its operations, with
// their requests and results, which the server implements and its clients
// call. The compiler checks both sides against it.
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
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/rest"
)

// The operations of the service.
var (
	CreateLink = tyr.Define[CreateReq, Created]("links.create", rest.Route("POST /links"), rest.Status(http.StatusCreated))
	GetLink    = tyr.Define[GetReq, Link]("links.get", rest.Route("GET /links/{code}"))
	FollowLink = tyr.Define[GetReq, FollowRes]("links.follow", rest.Route("GET /{code}"), rest.Status(http.StatusFound))
	DeleteLink = tyr.Define[DeleteReq, struct{}]("links.delete", rest.Route("DELETE /links/{code}"))
	PurgeLinks = tyr.Define[PurgeReq, PurgeRes]("links.purge") // no REST route: JSON-RPC only
)

// Link is a short link.
type Link struct {
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// Created is a link just created, with the path of its resource, which
// REST sends as the Location of 201 Created. JSON-RPC sends only the link.
type Created struct {
	Link
	Location string `json:"-" header:"Location"`
}

// CreateReq is a request to create a link. Without a code, the link gets a
// random one.
type CreateReq struct {
	URL  string `json:"url" validate:"required,http_url"`
	Code string `json:"code" validate:"omitempty,min=4,max=16"`
}

var codeRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// Validate holds the rule tags can't express: the characters of the code.
func (r CreateReq) Validate() error {
	var v tyr.Violations
	if r.Code != "" && !codeRe.MatchString(r.Code) {
		v.Add("code", "only a-z, 0-9 and '-'")
	}
	return v.Err()
}

// GetReq is a request for a link.
type GetReq struct {
	Code string `json:"code" path:"code" validate:"required"`
}

// FollowRes is where a link leads: REST redirects to it, JSON-RPC returns
// it.
type FollowRes struct {
	URL string `json:"url" header:"Location"`
}

// DeleteReq is a request to delete a link.
type DeleteReq struct {
	Code string `json:"code" path:"code" validate:"required"`
}

// PurgeReq is a request to delete the links to a host, such as one that
// serves malware.
type PurgeReq struct {
	Host string `json:"host" validate:"required,max=253"`
}

// PurgeRes is the result of a purge.
type PurgeRes struct {
	Purged int `json:"purged"` // the number of links deleted
}
