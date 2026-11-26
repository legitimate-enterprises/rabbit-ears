package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
)

// reconcileMoves gives each of the library's file records whose file is
// gone from its path the path the file is at now, before the scan would
// take it for a new file, so the titles it is filed under keep their ids
// and what points at them. A record an earlier scan made at the new path
// is folded in (see mediafile.Repo.Move), so a library scanned between a
// rename and now heals the same way.
//
// A gone file and a file on disk are the same by their fingerprint, which
// is of the media alone and so outlasts a new name, time, size or set of
// tags. Only one gone file and one file on disk sharing one are a move.
// A file at a path with no record is where a gone one went before any
// file that had its own record all along; only without one is a recorded
// file taken for the gone one's, and only when its size and modification
// time are the gone one's too, as an earlier scan's duplicate has and a
// copy kept beside it has not. More on either side could be each other,
// so nothing is moved and each gone file's title is left for review; a
// scan that could not walk every library folder keeps the reviews it
// does not raise again. A file without a fingerprint is no evidence.
//
// shortcut: a file found at a new path while any is gone is fingerprinted
// here and again when probed, carry the first over if such scans run long.
func (s *Scanner) reconcileMoves(ctx context.Context, lib *library.Library, folders []*library.Folder, cands []candidate, sum *Summary) error {
	files, err := s.Files.FilesInLibrary(lib.ID)
	if err != nil {
		return err
	}
	onDisk := make(map[string]bool, len(cands))
	for _, c := range cands {
		onDisk[c.path] = true
	}
	gone := map[string][]*mediafile.File{}
	recorded := make(map[string]*mediafile.File, len(files))
	ls := listings{}
	for _, f := range files {
		recorded[f.Path] = f
		if onDisk[f.Path] || !probe.Fingerprinted(f.Fingerprint) {
			continue
		}
		if ls.gone(rootOf(folders, f.Path), f.Path) {
			gone[f.Fingerprint] = append(gone[f.Fingerprint], f)
		}
	}
	arrived, known := map[string][]string{}, map[string][]string{}
	for _, c := range cands {
		if len(gone) == 0 {
			break
		}
		if f := recorded[c.path]; f != nil {
			if slices.ContainsFunc(gone[f.Fingerprint], func(g *mediafile.File) bool { return g.SizeBytes == c.size && g.ModifiedAt.Equal(c.mod) }) {
				known[f.Fingerprint] = append(known[f.Fingerprint], c.path)
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if fp := s.fingerprint(ctx, c.path); gone[fp] != nil {
			arrived[fp] = append(arrived[fp], c.path)
		}
	}
	var unsure []int64
	for k, olds := range gone {
		news := arrived[k]
		if len(news) == 0 {
			news = known[k]
		}
		switch {
		case len(news) == 0:
		case len(olds) == 1 && len(news) == 1:
			if err := s.move(lib, olds[0], news[0], sum); err != nil {
				return err
			}
		default:
			for _, f := range olds {
				if f.ItemID == 0 {
					continue
				}
				rv := &review.Review{LibraryID: lib.ID, ItemID: &f.ItemID, Reason: review.AmbiguousMove, RaisedAt: s.Now().UTC(),
					Evidence: []review.Evidence{{Role: review.Stated, Path: f.Path}}}
				for _, p := range news {
					rv.Evidence = append(rv.Evidence, review.Evidence{Role: review.Candidate, Path: p})
				}
				if err := s.raise(rv); err != nil {
					return err
				}
				unsure = append(unsure, rv.ID)
			}
		}
	}
	if s.Reviews == nil || sum.partial {
		return nil
	}
	return s.Reviews.Sweep(lib.ID, review.AmbiguousMove, unsure)
}

// rootOf is the library folder path is under, the filesystem's root for
// a path under none of them.
func rootOf(folders []*library.Folder, path string) string {
	root := string(filepath.Separator)
	for _, l := range folders {
		if rel, err := filepath.Rel(l.Path, path); err == nil && filepath.IsLocal(rel) {
			root = l.Path
		}
	}
	return root
}

// listings are the names in each directory read, nil for one that cannot
// be read, so many paths in one directory cost one read.
type listings map[string]map[string]bool

// gone reports whether path, under root, is not on disk by exactly its
// name: some directory from root down does not list the next part of
// path as path spells it, so a name changed only in case is gone even on
// a volume that ignores case. A directory that cannot be read, root
// included, leaves path not gone, as an unmounted volume would.
func (l listings) gone(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return false
	}
	dir := root
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		names, ok := l[dir]
		if !ok {
			if entries, err := os.ReadDir(dir); err == nil {
				names = make(map[string]bool, len(entries))
				for _, e := range entries {
					names[e.Name()] = true
				}
			}
			l[dir] = names
		}
		if names == nil {
			return false
		}
		if !names[name] {
			return true
		}
		dir = filepath.Join(dir, name)
	}
	return false
}

// move is one file found at a new path. A song's and a video's path says
// what they are, so theirs are read again.
func (s *Scanner) move(lib *library.Library, f *mediafile.File, to string, sum *Summary) error {
	from := f.Path
	prune, err := s.Files.Move(f, to, lib.Kind == library.Music || lib.Kind == library.YouTube)
	if err != nil {
		return err
	}
	logstore.Log("scan").Info("Moved file keeps its record", "from", from, "to", to, "item_id", f.ItemID)
	sum.moved[to] = true
	for _, id := range prune {
		if err := s.pruneEmpty(id, sum); err != nil {
			return err
		}
	}
	return nil
}

// raise records a review; a scanner without a review repository keeps none.
func (s *Scanner) raise(rv *review.Review) error {
	if s.Reviews == nil {
		return nil
	}
	return s.Reviews.Raise(rv)
}

// homed returns the show or channel whose folder dir is, for a file of
// it at path. That is the one recorded at dir, unless the file is already
// filed under another whose own folder is gone: then that one moved here
// with its file, and takes dir from the one recorded there. One all of
// whose files are in dir was made of the folder's name by an earlier scan
// and is the same show or channel twice, so what is under it comes too;
// one with files elsewhere is another, put here by a guess, and only
// loses the folder. db.ErrNotFound when neither is known.
func (s *Scanner) homed(lib *library.Library, kind, dir, path string) (*item.Item, error) {
	at, err := s.Items.ByPath(lib.ID, kind, dir)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return nil, err
	}
	f, ferr := s.Files.FileByPath(path)
	if ferr != nil {
		return at, err
	}
	owner, oerr := s.Items.HomeOf(f.ID, kind)
	if oerr != nil || at != nil && owner.ID == at.ID {
		return at, err
	}
	if owner.Path == "" || !(listings{}).gone(filepath.Dir(owner.Path), owner.Path) {
		return at, err
	}
	if at != nil {
		elsewhere, err := s.Files.Outside(at.ID, dir)
		if err != nil {
			return nil, err
		}
		if at.Path = ""; elsewhere == 0 {
			err = s.Files.Absorb(owner.ID, at.ID)
		} else {
			err = s.Items.Update(at)
		}
		if err != nil {
			return nil, err
		}
	}
	logstore.Log("scan").Info("Moved folder keeps its record", "from", owner.Path, "to", dir, "item_id", owner.ID)
	owner.Path = dir
	return owner, s.Items.Update(owner)
}
