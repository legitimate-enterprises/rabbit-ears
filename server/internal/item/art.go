package item

import (
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// ArtURL is the path getArtwork serves under; an image's URL is ArtURL
// followed by its address, "store/name".
//
// An address names one image for good: a store's file names are TMDB's own
// or a content hash, so what an item shows changes only by its address
// changing, and clients cache each address forever.
const ArtURL = "/api/v2/artwork/"

// Art is the URLs of an item's poster and backdrop, "" for none.
type Art struct{ Poster, Backdrop string }

// StoreRef is the address of the image path p in a store other than
// TMDB's, read as the server stores it (relative to the data directory) or
// as a replica carries it ("store/name"); ” for a path in none.
func StoreRef(p string) string {
	return `CASE WHEN ` + p + ` LIKE 'images/caa/%' OR ` + p + ` LIKE 'thumbnails/local/%' OR ` + p + ` LIKE 'thumbnails/music/%'
			OR ` + p + ` LIKE 'cache/youtube/%' THEN substr(` + p + `, instr(` + p + `, '/') + 1)
		WHEN ` + p + ` LIKE 'caa/%' OR ` + p + ` LIKE 'local/%' OR ` + p + ` LIKE 'music/%' OR ` + p + ` LIKE 'youtube/%' THEN ` + p + `
		ELSE '' END`
}

// level is the address of the image col (poster or backdrop) item id shows
// under the metadata settings: NULL without one, so the next level is read,
// and ” for one in no store, which shows none.
func level(id, col, size string) string {
	p := `m.` + col + `_path`
	return `(SELECT CASE WHEN COALESCE(` + p + `, '') = '' THEN NULL WHEN m.` + col + `_from = 'tmdb' THEN '` + size + `' || ` + p + `
		ELSE ` + StoreRef(p) + ` END FROM item_shown m WHERE m.item_id = ` + id + `)`
}

// chain is item t's image col: its own, else its parent's, else its
// grandparent's, NULL when none has one.
func chain(t, col, size string) string {
	return `COALESCE(` + level(t+`.id`, col, size) + `, ` + level(t+`.parent_id`, col, size) + `,
		` + level(`(SELECT g.parent_id FROM item g WHERE g.id = `+t+`.parent_id)`, col, size) + `)`
}

// PosterOf is the address of item t's poster, ” for none. An artist's is
// its newest album's that has one, of those clean of explicit songs while
// clean holds.
func PosterOf(t, clean string) string {
	album := chain("al", "poster", "w500")
	return `COALESCE(CASE WHEN ` + t + `.kind = 'artist' THEN (SELECT ` + album + ` FROM item al
			WHERE al.parent_id = ` + t + `.id AND al.kind = 'album' AND NULLIF(` + album + `, '') IS NOT NULL
				AND (NOT ` + clean + ` OR ` + CleanOf("al") + `)
			ORDER BY al.parsed_year IS NULL, al.parsed_year DESC, ` + sortKey("al") + `, al.id LIMIT 1)
		ELSE ` + chain(t, "poster", "w500") + ` END, '')`
}

// BackdropOf is the address of item t's backdrop, ” for none; an artist
// has none.
func BackdropOf(t string) string {
	return `CASE WHEN ` + t + `.kind = 'artist' THEN '' ELSE COALESCE(` + chain(t, "backdrop", "w1280") + `, '') END`
}

// URLOf is the URL of address ref, NULL for ”.
func URLOf(ref string) string { return `'` + ArtURL + `' || NULLIF(` + ref + `, '')` }

// Art is the art of each of ids, an artist's as a kid hears it when kid.
func (r *Repo) Art(ids []uid.ID, kid bool) (map[uid.ID]Art, error) {
	out := map[uid.ID]Art{}
	if len(ids) == 0 {
		return out, nil
	}
	clean := "0"
	if kid {
		clean = "1"
	}
	in, args := db.In(ids)
	rows, err := r.d.Query(`SELECT i.id, COALESCE(`+URLOf(PosterOf("i", clean))+`, ''), COALESCE(`+URLOf(BackdropOf("i"))+`, '')
		FROM item i WHERE i.id IN `+in, args...)
	if err != nil {
		return nil, db.Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uid.ID
		var a Art
		if err := rows.Scan(&id, &a.Poster, &a.Backdrop); err != nil {
			return nil, db.Wrap(err)
		}
		out[id] = a
	}
	return out, db.Wrap(rows.Err())
}

// NamesTMDB reports whether the data shows TMDB's image path at size: a
// remote TMDB row or a collection as a w500 poster or w1280 backdrop, a
// person as a w185 headshot.
func (r *Repo) NamesTMDB(size, path string) (bool, error) {
	q := `SELECT EXISTS (SELECT 1 FROM person WHERE profile_path = ?1)`
	if col := map[string]string{"w500": "poster_path", "w1280": "backdrop_path"}[size]; col != "" {
		q = `SELECT EXISTS (SELECT 1 FROM item_metadata WHERE source = 'remote' AND provider = 'tmdb' AND ` + col + ` = ?1)
			OR EXISTS (SELECT 1 FROM collection WHERE ` + col + ` = ?1)`
	} else if size != "w185" {
		return false, nil
	}
	var ok bool
	return ok, db.Wrap(r.d.QueryRow(q, path).Scan(&ok))
}
