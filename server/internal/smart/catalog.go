package smart

// Field types: what operators a field takes and whether a condition carries
// text, a number or neither.
const (
	text = iota
	number
	boolean
)

// field is one thing a condition can test, as SQL over a leaf. A scalar's
// expr is the value; a list's from is "FROM … WHERE <its link to the leaf>"
// and expr its element's value there. personal reads the owner, bound at
// ?1.
type field struct {
	typ      int
	personal bool
	expr     string
	from     string
}

// shown is column col of item_shown for item alias a.
func shown(col, a string) string {
	return `(SELECT m.` + col + ` FROM item_shown m WHERE m.item_id = ` + a + `.id)`
}

// titleOf is the title item alias a shows: its metadata's, else the parsed one.
func titleOf(a string) string {
	return `COALESCE(NULLIF(` + shown("title", a) + `, ''), ` + a + `.parsed_title)`
}

func yearOf(a string) string {
	return `COALESCE(CAST(NULLIF(substr(` + shown("release_date", a) + `, 1, 4), '') AS INTEGER), ` + a + `.parsed_year)`
}

func runtimeOf(a string) string {
	return `COALESCE(` + shown("runtime_minutes", a) + `, (SELECT MAX(e.runtime_ms) FROM edition e WHERE e.item_id = ` + a + `.id) / 60000)`
}

// genreOf is the genres the items in set (an SQL list) show.
func genreOf(set string) field {
	return field{typ: text, from: `FROM item_genre_shown g JOIN genre n ON n.id = g.genre_id WHERE g.item_id IN (` + set + `)`, expr: `n.name`}
}

func creditOf(a, which string) field {
	return field{typ: text, from: `FROM credit_shown c JOIN person p ON p.id = c.person_id WHERE c.item_id = ` + a + `.id AND ` + which,
		expr: `p.name`}
}

// fileOf is whether item alias a has a file not missing with a track of
// kind matching cond.
func fileOf(a, kind, cond string) string {
	return `EXISTS (SELECT 1 FROM item_file x JOIN media_file f ON f.id = x.media_file_id AND f.missing_at IS NULL
		JOIN track t ON t.media_file_id = f.id WHERE x.item_id = ` + a + `.id AND t.kind = '` + kind + `' AND ` + cond + `)`
}

func watched(a string) field {
	return field{typ: boolean, personal: true, expr: `(SELECT w.watched FROM watch_state w WHERE w.account_id = ?1 AND w.item_id = ` + a + `.id)`}
}

func playCount(a string) field {
	return field{typ: number, personal: true,
		expr: `COALESCE((SELECT w.play_count FROM watch_state w WHERE w.account_id = ?1 AND w.item_id = ` + a + `.id), 0)`}
}

// favorite is whether the owner starred any item of set (an SQL list).
func favorite(set string) field {
	return field{typ: boolean, personal: true, expr: `EXISTS (SELECT 1 FROM favorite f WHERE f.account_id = ?1 AND f.item_id IN (` + set + `))`}
}

var (
	hdr = field{typ: boolean, expr: fileOf("item", "video", "t.hdr_format IS NOT NULL")}
	// uhd is edition.Copy.Quality's 4K.
	uhd = field{typ: boolean, expr: fileOf("item", "video", "(t.width >= 3200 OR t.height >= 2000)")}
)

var movieFields = map[string]field{
	"title":         {typ: text, expr: titleOf("item")},
	"year":          {typ: number, expr: yearOf("item")},
	"runtime":       {typ: number, expr: runtimeOf("item")},
	"contentRating": {typ: text, expr: shown("content_rating", "item")},
	"genre":         genreOf("item.id"),
	"cast":          creditOf("item", "c.kind = 'cast'"),
	"director":      creditOf("item", "c.kind = 'crew' AND c.job = 'Director'"),
	"hdr":           hdr,
	"uhd":           uhd,
	"watched":       watched("item"),
	"playCount":     playCount("item"),
	"favorite":      favorite("item.id"),
}

// catalog is the fields of each kind of list: a playlist's kind, or
// Collection. A movies list's leaf is item; a tv list's is the episode
// item, its season se and show sh; a music list's the song s, its album al,
// the album's artist ar and the song's own artist pa.
var catalog = map[string]map[string]field{
	"movies": movieFields,
	Collection: func() map[string]field {
		out := map[string]field{}
		for k, f := range movieFields {
			if !f.personal {
				out[k] = f
			}
		}
		return out
	}(),
	"tv": {
		"title":         {typ: text, expr: titleOf("item")},
		"show":          {typ: text, expr: titleOf("sh")},
		"season":        {typ: number, expr: `se.season_number`},
		"episode":       {typ: number, expr: `item.episode_number`},
		"year":          {typ: number, expr: yearOf("item")},
		"runtime":       {typ: number, expr: runtimeOf("item")},
		"contentRating": {typ: text, expr: shown("content_rating", "sh")},
		"genre":         genreOf("sh.id"),
		"cast":          creditOf("sh", "c.kind = 'cast'"),
		"hdr":           hdr,
		"uhd":           uhd,
		"watched":       watched("item"),
		"playCount":     playCount("item"),
		"favorite":      favorite("item.id, se.id, sh.id"),
	},
	"music": {
		"title":  {typ: text, expr: `s.parsed_title`},
		"artist": {typ: text, expr: `COALESCE(pa.parsed_title, ar.parsed_title)`},
		"album":  {typ: text, expr: `al.parsed_title`},
		"year":   {typ: number, expr: `COALESCE(s.parsed_year, al.parsed_year)`},
		"genre":  genreOf("al.id, ar.id"),
		"duration": {typ: number, expr: `(SELECT f.duration_ms FROM item_file x JOIN media_file f ON f.id = x.media_file_id
			WHERE x.item_id = s.id ORDER BY f.id LIMIT 1) / 1000`},
		"explicit": {typ: boolean, expr: `s.explicit`},
		// mediafile.Lossless over any audio track of the song.
		"lossless":  {typ: boolean, expr: fileOf("s", "audio", `(t.codec IN ('flac','alac','wavpack') OR substr(t.codec, 1, 4) = 'pcm_')`)},
		"playCount": playCount("s"),
		"favorite":  favorite("s.id, al.id, ar.id, s.artist_id"),
	},
}

// leaves is, for each kind of list, the query of every title it may hold,
// in the order it holds them, with %s standing for the rules.
var leaves = map[string]string{
	"movies": `SELECT item.id FROM item WHERE item.kind = 'movie' AND (%s)
		ORDER BY COALESCE(item.sort_title, item.parsed_title) COLLATE NOCASE, item.id`,
	"tv": `SELECT item.id FROM item CROSS JOIN item se ON se.id = item.parent_id CROSS JOIN item sh ON sh.id = se.parent_id
		WHERE item.kind = 'episode' AND (%s)
		ORDER BY COALESCE(sh.sort_title, sh.parsed_title) COLLATE NOCASE, sh.id, COALESCE(se.season_number, 0) = 0, se.season_number, se.id,
			item.episode_number, item.id`,
	"music": `SELECT s.id FROM item s CROSS JOIN item al ON al.id = s.parent_id CROSS JOIN item ar ON ar.id = al.parent_id
		LEFT JOIN item pa ON pa.id = s.artist_id WHERE s.kind = 'song' AND (%s)
		ORDER BY COALESCE(pa.sort_title, pa.parsed_title, ar.sort_title, ar.parsed_title) COLLATE NOCASE,
			COALESCE(al.sort_title, al.parsed_title) COLLATE NOCASE, al.id, COALESCE(s.disc_number, 1), s.track_number IS NULL, s.track_number,
			COALESCE(s.sort_title, s.parsed_title) COLLATE NOCASE, s.id`,
	Collection: `SELECT item.id FROM item WHERE item.kind = 'movie' AND (%s) ORDER BY item.id`,
}
