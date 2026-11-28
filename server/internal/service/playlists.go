package service

import (
	"errors"
	"slices"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

var (
	// ErrPlaylistKindLocked means a playlist with entries cannot change class.
	ErrPlaylistKindLocked = errors.New("a playlist with entries cannot change its kind")
	// ErrWrongPlaylistKind means the item is not of the class the playlist holds.
	ErrWrongPlaylistKind = errors.New("the item does not belong in this kind of playlist")
	// ErrEditionRequired means the title has several editions and none was named.
	ErrEditionRequired = errors.New("the title has several editions; name one")
	// ErrNotItsEdition means the edition is not one of the entry's title.
	ErrNotItsEdition = errors.New("not an edition of this movie, episode or song")
	// ErrNotShared means the playlist is neither the profile's nor shared with it.
	ErrNotShared = errors.New("not your playlist and not shared with you")
	// ErrAccessDenied means the profile's access to the playlist does not reach the request.
	ErrAccessDenied = errors.New("your access to this playlist does not allow that")
	// ErrShareTarget means the profile is not one the owner may share with.
	ErrShareTarget = errors.New("share with someone you see: a member of a household of yours, or an adult linked with you")
	// ErrHouseholdShareTarget means the household is not one the owner is in.
	ErrHouseholdShareTarget = errors.New("share with a household you are in")
	// ErrKidReadsOnly means a kid was given write access.
	ErrKidReadsOnly = errors.New("a kid only reads a playlist")
	// ErrStationPlaylist means the playlist is a station's own.
	ErrStationPlaylist = errors.New("a station's own playlist is not shared")
	// ErrSmartPlaylist means a smart playlist's rules choose its entries and
	// fix its kind.
	ErrSmartPlaylist = errors.New("a smart playlist's entries follow its rules")
	// ErrPlaylistOnAir means one of the owner's stations plays the playlist.
	ErrPlaylistOnAir = errors.New("a station plays this playlist; delete the station or give it another playlist first")
)

// Owner is the access the owner of a playlist has, beyond playlist.Write.
const Owner = "owner"

// reach orders the accesses, each allowing what the ones before it do.
var reach = map[string]int{playlist.Read: 0, playlist.Write: 1, Owner: 2}

// PlaylistService manages each profile's movies, tv and music playlists,
// and who they are shared with.
type PlaylistService struct {
	repo     *playlist.Repo
	editions *edition.Repo
	stations *station.Repo
	accounts *account.Repo
}

// NewPlaylistService returns a PlaylistService over its repositories.
func NewPlaylistService(repo *playlist.Repo, editions *edition.Repo, stations *station.Repo, accounts *account.Repo) *PlaylistService {
	return &PlaylistService{repo, editions, stations, accounts}
}

// Access is what the profile may do with p: Owner, or the strongest access
// its shares give the profile, directly or through a household it is in,
// else ErrNotShared.
func (s *PlaylistService) Access(p *playlist.Playlist, accountID uid.ID) (string, error) {
	if p.AccountID == accountID {
		return Owner, nil
	}
	got, err := s.repo.Access(p.ID, accountID)
	if err == nil && got == "" {
		err = ErrNotShared
	}
	return got, err
}

// Check returns the profile's access to p if it reaches need, else
// ErrNotShared or ErrAccessDenied.
func (s *PlaylistService) Check(p *playlist.Playlist, accountID uid.ID, need string) (string, error) {
	got, err := s.Access(p, accountID)
	if err == nil && reach[got] < reach[need] {
		err = ErrAccessDenied
	}
	return got, err
}

// shareable checks that the owner of p may give to access: to must be
// one of the owner's People on the calling device, a TV or not.
func (s *PlaylistService) shareable(p *playlist.Playlist, owner, to *account.Account, access string, tv bool) error {
	if p.StationOwned {
		return ErrStationPlaylist
	}
	sees, err := s.accounts.Sees(owner, to.ID, tv)
	switch {
	case err != nil:
		return err
	case !sees:
		return ErrShareTarget
	case to.Kind == account.Kid && access != playlist.Read:
		return ErrKidReadsOnly
	}
	return nil
}

// Share shares p, owned by owner, with to, from a TV or not; a second
// share with them is db.ErrConflict.
func (s *PlaylistService) Share(p *playlist.Playlist, owner, to *account.Account, access string, tv bool) (*playlist.Share, error) {
	if err := s.shareable(p, owner, to, access, tv); err != nil {
		return nil, err
	}
	sh := &playlist.Share{PlaylistID: p.ID, AccountID: to.ID, Access: access}
	return sh, s.repo.CreateShare(sh)
}

// SetAccess changes sh, a share with to, to access.
func (s *PlaylistService) SetAccess(sh *playlist.Share, to *account.Account, access string) error {
	if to.Kind == account.Kid && access != playlist.Read {
		return ErrKidReadsOnly
	}
	sh.Access = access
	return s.repo.UpdateShare(sh)
}

// Unshare stops sharing p with the profile and, unless a household share
// still reaches it, deletes the profile's stations that play it.
func (s *PlaylistService) Unshare(p *playlist.Playlist, accountID uid.ID) error {
	if err := s.repo.DeleteShare(p.ID, accountID); err != nil {
		return err
	}
	return s.stations.DeleteUnseen(&p.ID, &accountID)
}

// ShareHousehold shares p, owned by owner, with everyone in a household
// owner is in; a second share with it is db.ErrConflict.
func (s *PlaylistService) ShareHousehold(p *playlist.Playlist, owner *account.Account, householdID uid.ID, access string) (*playlist.HouseholdShare, error) {
	if p.StationOwned {
		return nil, ErrStationPlaylist
	}
	in, err := s.accounts.Households(owner)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(in, householdID) {
		return nil, ErrHouseholdShareTarget
	}
	sh := &playlist.HouseholdShare{PlaylistID: p.ID, HouseholdID: householdID, Access: access}
	return sh, s.repo.CreateHouseholdShare(sh)
}

// UnshareHousehold stops sharing p with the household and deletes the
// stations that play it of those it no longer reaches.
func (s *PlaylistService) UnshareHousehold(p *playlist.Playlist, householdID uid.ID) error {
	if err := s.repo.DeleteHouseholdShare(p.ID, householdID); err != nil {
		return err
	}
	return s.stations.DeleteUnseen(&p.ID, nil)
}

// Delete deletes p with its entries and shares, and the stations of those
// it was shared with that play it; one of the owner's own stations playing
// it is ErrPlaylistOnAir.
func (s *PlaylistService) Delete(p *playlist.Playlist) error {
	if used, err := s.stations.UsesPlaylist(p.ID, p.AccountID); err != nil {
		return err
	} else if used {
		return ErrPlaylistOnAir
	}
	if err := s.stations.DeletePlaying(p.ID, nil); err != nil {
		return err
	}
	return s.repo.Delete(p.ID)
}

// Repo exposes the repository.
func (s *PlaylistService) Repo() *playlist.Repo { return s.repo }

// holds is what each kind of playlist takes; nothing from YouTube fits any.
var holds = map[string][]string{
	playlist.Movies: {item.Movie},
	playlist.TV:     {item.Show, item.Season, item.Episode},
	playlist.Music:  {item.Artist, item.Album, item.Song},
}

// Holds reports whether a playlist of kind takes an item of itemKind.
func Holds(kind, itemKind string) bool { return slices.Contains(holds[kind], itemKind) }

// Rename sets p's name and kind. A playlist with entries keeps its kind.
func (s *PlaylistService) Rename(p *playlist.Playlist, name, kind string, by *uid.ID) error {
	if kind != p.Kind && p.Smart {
		return ErrSmartPlaylist
	}
	if kind != p.Kind {
		n, err := s.repo.CountEntries(p.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrPlaylistKindLocked
		}
	}
	p.Name, p.Kind = name, kind
	return s.repo.Update(p, by)
}

// AddEntry puts it into p at rank, or with none at position at (nil
// appends), if p can hold it, as EntryFor describes. The entry takes id
// when given.
func (s *PlaylistService) AddEntry(p *playlist.Playlist, it *item.Item, editionID *uid.ID, at *int, rank string, id, by *uid.ID) (*playlist.Entry, error) {
	e, err := s.EntryFor(p, it, editionID)
	if err != nil {
		return nil, err
	}
	e.Rank = rank
	if id != nil {
		e.ID = *id
	}
	return e, s.repo.AddEntry(e, at, by)
}

// EntryFor is the entry that adds it to p, written nowhere. A movie, episode
// or song plays editionID (nil for none named), which must be its own; with
// none named, its only edition, and a title with several needs one named. A
// container takes no edition.
func (s *PlaylistService) EntryFor(p *playlist.Playlist, it *item.Item, editionID *uid.ID) (*playlist.Entry, error) {
	if !Holds(p.Kind, it.Kind) {
		return nil, ErrWrongPlaylistKind
	}
	e := &playlist.Entry{PlaylistID: p.ID, ItemID: ref(it.ID), EditionID: editionID}
	title := it.Kind == item.Movie || it.Kind == item.Episode || it.Kind == item.Song
	switch {
	case !title && editionID != nil:
		return nil, ErrNotItsEdition
	case editionID != nil:
		ed, err := s.editions.Get(*editionID)
		if errors.Is(err, db.ErrNotFound) || err == nil && ed.ItemID != it.ID {
			return nil, ErrNotItsEdition
		} else if err != nil {
			return nil, err
		}
	case title:
		eds, err := s.editions.ForItem(it.ID)
		if err != nil {
			return nil, err
		}
		if len(eds) > 1 {
			return nil, ErrEditionRequired
		}
		if len(eds) == 1 {
			e.EditionID = ref(eds[0].ID)
		}
	}
	return e, nil
}

// AddCollection puts collection c into p at rank, or with none at position
// at (nil appends), if p is a movies playlist. The entry takes id when given.
func (s *PlaylistService) AddCollection(p *playlist.Playlist, c *collection.Collection, at *int, rank string, id, by *uid.ID) (*playlist.Entry, error) {
	if p.Kind != playlist.Movies {
		return nil, ErrWrongPlaylistKind
	}
	e := &playlist.Entry{PlaylistID: p.ID, CollectionID: ref(c.ID), Rank: rank}
	if id != nil {
		e.ID = *id
	}
	return e, s.repo.AddEntry(e, at, by)
}
