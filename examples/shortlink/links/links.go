// Package links is the business logic of the service: it creates short
// links, follows and deletes them, and purges the ones to a host. Its
// requests and results are those of the contract.
package links

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/tyr-go/tyr"
	"github.com/tyr-go/tyr/examples/shortlink/contract"
	"github.com/tyr-go/tyr/examples/shortlink/store"
)

// Service implements the operations on links.
type Service struct {
	store   *store.Store
	newCode func() string
}

// New returns a service that keeps links in s.
func New(s *store.Store) *Service {
	return &Service{store: s, newCode: randomCode}
}

// Create creates a link.
func (s *Service) Create(ctx context.Context, req contract.CreateReq) (contract.Created, error) {
	l := store.Link{Code: req.Code, URL: req.URL, CreatedAt: time.Now().UTC()}
	var err error
	if l.Code != "" {
		err = s.store.Create(ctx, l)
	} else {
		// A random code is rarely taken; if it is, try another.
		for range 3 {
			l.Code = s.newCode()
			if err = s.store.Create(ctx, l); !errors.Is(err, store.ErrExists) {
				break
			}
		}
		if errors.Is(err, store.ErrExists) {
			// The client didn't choose the code, so this is no conflict of
			// its making, but an internal error. As a *tyr.Error, it skips
			// the mappers, which would report ErrExists as a conflict, and
			// keeps ErrExists as its cause for the logs.
			err = tyr.Internal("links: no free random code").WithCause(err)
		}
	}
	if err != nil {
		return contract.Created{}, err
	}
	slog.InfoContext(ctx, "link created", "code", l.Code)
	return contract.Created{Link: linkOf(l), Location: "/links/" + l.Code}, nil
}

// Get returns a link.
func (s *Service) Get(ctx context.Context, req contract.GetReq) (contract.Link, error) {
	l, err := s.store.Get(ctx, req.Code)
	if err != nil {
		return contract.Link{}, err
	}
	return linkOf(l), nil
}

// Follow returns where a link leads.
func (s *Service) Follow(ctx context.Context, req contract.GetReq) (contract.FollowRes, error) {
	l, err := s.store.Get(ctx, req.Code)
	if err != nil {
		return contract.FollowRes{}, err
	}
	return contract.FollowRes{URL: l.URL}, nil
}

// Delete deletes a link.
func (s *Service) Delete(ctx context.Context, req contract.DeleteReq) (struct{}, error) {
	if err := s.store.Delete(ctx, req.Code); err != nil {
		return struct{}{}, err
	}
	slog.InfoContext(ctx, "link deleted", "code", req.Code)
	return struct{}{}, nil
}

// Purge deletes the links to a host.
func (s *Service) Purge(ctx context.Context, req contract.PurgeReq) (contract.PurgeRes, error) {
	n := s.store.DeleteFunc(ctx, func(l store.Link) bool {
		u, err := url.Parse(l.URL)
		return err == nil && strings.EqualFold(u.Hostname(), req.Host)
	})
	slog.InfoContext(ctx, "links purged", "host", req.Host, "purged", n)
	return contract.PurgeRes{Purged: n}, nil
}

// randomCode returns a random code of 7 characters of a-z and 2-7.
func randomCode() string {
	return strings.ToLower(rand.Text()[:7])
}

// linkOf returns the stored link l as a link of the contract.
func linkOf(l store.Link) contract.Link {
	return contract.Link{Code: l.Code, URL: l.URL, CreatedAt: l.CreatedAt}
}
