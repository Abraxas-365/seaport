package hostingsvc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"time"

	"github.com/Abraxas-365/seaport/internal/blob"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/Abraxas-365/seaport/internal/text"
)

// homeSealInfo derives the key that seals homes at rest (from
// SEAPORT_SECRET_KEY; a new info re-keys).
const homeSealInfo = "seaport homes v1"

// Homes is what the service needs to keep harness homes: blob storage and
// the secret key that seals them (either missing: uploads refused).
type Homes struct {
	Blobs     blob.Store
	SecretKey string
}

type homes struct {
	blobs  blob.Store
	sealer *seal.Sealer
}

func newHomes(h Homes) (homes, error) {
	if h.Blobs == nil || h.SecretKey == "" {
		return homes{}, nil
	}
	s, err := seal.New(h.SecretKey, homeSealInfo)
	if err != nil {
		return homes{}, err
	}
	return homes{blobs: h.Blobs, sealer: &s}, nil
}

// homePurpose binds a sealed home to its organization and digest, so a
// blob copied under another row doesn't open.
func homePurpose(org identity.OrganizationID, digest string) string {
	return "home/" + org.String() + "/" + digest
}

func (s *Service) UploadHome(ctx context.Context, org identity.OrganizationID, input hosting.UploadHome) (hosting.Home, error) {
	if err := input.Validate(); err != nil {
		return hosting.Home{}, err
	}
	if s.homes.sealer == nil {
		return hosting.Home{}, errx.Business("homes need blob storage on this port (SEAPORT_BLOB_URL)")
	}
	b, err := hosting.ReadHome(input.Harness, input.Archive)
	if err != nil {
		return hosting.Home{}, err
	}
	if h, err := s.repo.Home(ctx, org, b.Digest); err == nil {
		// known: just touch it (no second blob)
		stored, _, err := s.repo.AddHome(ctx, org, h, hosting.MaxHomes)
		return stored, err
	} else if !errx.IsType(err, errx.TypeNotFound) {
		return hosting.Home{}, errx.Wrap(err, "find home", errx.TypeInternal)
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	h := hosting.Home{Organization: org, Digest: b.Digest, Harness: input.Harness, Files: b.Files, Size: b.Size,
		Key: blob.TenantKey(org, "homes", hex.EncodeToString(id[:])), CreatedBy: text.Clean(input.CreatedBy)}
	// Staged first: a blob whose row never comes (a crash, a lost race) is
	// deleted by the sweeper.
	if err := s.repo.StageHomeBlob(ctx, org, h.Key, time.Now().Add(hosting.HomeGrace)); err != nil {
		return hosting.Home{}, errx.Wrap(err, "stage home blob", errx.TypeInternal)
	}
	sealed := s.homes.sealer.Seal(homePurpose(org, h.Digest), b.Tar)
	if err := s.homes.blobs.Put(ctx, h.Key, bytes.NewReader(sealed), blob.PutOptions{ContentType: "application/octet-stream"}); err != nil {
		return hosting.Home{}, errx.Wrap(err, "store home", errx.TypeInternal)
	}
	stored, _, err := s.repo.AddHome(ctx, org, h, hosting.MaxHomes)
	if err != nil {
		return hosting.Home{}, errx.Wrap(err, "add home", errx.TypeInternal)
	}
	return stored, nil
}

func (s *Service) Home(ctx context.Context, org identity.OrganizationID, digest string) (hosting.Home, error) {
	if !hosting.ValidHomeDigest(digest) {
		return hosting.Home{}, errx.InvalidField("digest", "a home is named sha256:<digest>")
	}
	return s.repo.Home(ctx, org, digest)
}

func (s *Service) HomeArchive(ctx context.Context, org identity.OrganizationID, digest string) ([]byte, error) {
	h, err := s.Home(ctx, org, digest)
	if err != nil {
		return nil, err
	}
	return s.openHome(ctx, h)
}

func (s *Service) HomeFile(ctx context.Context, org identity.OrganizationID, digest, path string) ([]byte, error) {
	if err := hosting.HomeFilePath(path); err != nil {
		return nil, err
	}
	tarball, err := s.HomeArchive(ctx, org, digest)
	if err != nil {
		return nil, err
	}
	return hosting.HomeFileData(tarball, path)
}

// openHome reads and unseals a home's tar.
func (s *Service) openHome(ctx context.Context, h hosting.Home) ([]byte, error) {
	if s.homes.sealer == nil {
		return nil, errx.Business("homes need blob storage on this port (SEAPORT_BLOB_URL)")
	}
	r, err := s.homes.blobs.Get(ctx, h.Key)
	if err != nil {
		return nil, errx.Wrap(err, "read home", errx.TypeInternal)
	}
	defer r.Close()
	sealed, err := io.ReadAll(io.LimitReader(r, hosting.MaxHomeBytes*2))
	if err != nil {
		return nil, errx.Wrap(err, "read home", errx.TypeInternal)
	}
	tarball, err := s.homes.sealer.Open(homePurpose(h.Organization, h.Digest), sealed)
	if err != nil {
		return nil, errx.Wrap(err, "open home", errx.TypeInternal)
	}
	return tarball, nil
}

// buildHome is the tar a build bakes in for spec's home (nil: none).
func (s *Service) buildHome(ctx context.Context, org identity.OrganizationID, spec hosting.Spec) ([]byte, error) {
	if spec.Home == "" {
		return nil, nil
	}
	h, err := s.repo.Home(ctx, org, spec.Home)
	if err != nil {
		return nil, err
	}
	if h.Harness != spec.Harness {
		return nil, errx.Business("the environment's home is for " + h.Harness + ", its harness is " + spec.Harness)
	}
	return s.openHome(ctx, h)
}

func (s *Service) SweepHomes(ctx context.Context, now time.Time) (int, error) {
	before := now.Add(-hosting.HomeGrace)
	stale, err := s.repo.StaleHomes(ctx, before, hosting.MaxHomeSweep)
	if err != nil {
		return 0, errx.Wrap(err, "stale homes", errx.TypeInternal)
	}
	n := 0
	for _, h := range stale {
		if err := s.repo.DeleteHome(ctx, h.Organization, h.Digest, before); err != nil {
			slog.Warn("delete unused home; retrying later", "err", err)
			continue
		}
		n++
	}
	return n, nil
}
