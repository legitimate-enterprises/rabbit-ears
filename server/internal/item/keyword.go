package item

import "github.com/legitimate-enterprises/rabbit-ears/server/internal/db"

// SetKeywords replaces a movie's or show's TMDB keyword ids.
func (r *Repo) SetKeywords(itemID int64, ids []int64) error {
	return db.InTx(r.d, func(tx db.DBTX) error {
		if _, err := tx.Exec(`DELETE FROM item_keyword WHERE item_id = ?`, itemID); err != nil {
			return db.Wrap(err)
		}
		for _, id := range ids {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO item_keyword (item_id, keyword_id) VALUES (?, ?)`, itemID, id); err != nil {
				return db.Wrap(err)
			}
		}
		return nil
	})
}

// OccasionMembersOf selects the ids of the members of the occasion bound
// at p: movies and shows carrying any of its keywords while the metadata
// settings use remote data, or added by hand, less those removed by hand.
func OccasionMembersOf(p string) string {
	return `SELECT om.id FROM item om WHERE om.kind IN ('movie','show')
		AND om.id NOT IN (SELECT ox.item_id FROM occasion_exclusion ox WHERE ox.occasion_id = ` + p + `)
		AND (om.id IN (SELECT oa.item_id FROM occasion_item oa WHERE oa.occasion_id = ` + p + `)
			OR om.id IN (SELECT ik.item_id FROM item_keyword_shown ik JOIN occasion_keyword ok ON ok.keyword_id = ik.keyword_id
				WHERE ok.occasion_id = ` + p + `))`
}
