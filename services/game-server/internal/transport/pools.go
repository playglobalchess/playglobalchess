package transport

// PoolCatalog maps each pool id a client may SEEK into to the one time control
// that pool plays (GS5 §3.1: a pool encodes its time control). The server, not
// the client, decides a game's clock: SEEK into an unknown pool, or with a
// time_control other than the pool's, is refused before enqueue (#93).
//
// Interim (GS6 §3.2): the source of truth is the REST pools catalogue
// (GET /v1/pools, X-13), which is not built yet. Until it is, the server uses
// this fixed allow-list; the X-13 catalogue replaces it with no client change.
type PoolCatalog map[string]string

// DefaultPools is the interim allow-list. Each pool id IS its time-control id,
// so the pair is unambiguous for clients: a common chess.com-style set of
// bullet, blitz, rapid and daily controls.
func DefaultPools() PoolCatalog {
	ids := []string{
		"bullet-1+0", "bullet-2+1",
		"blitz-3+0", "blitz-3+2", "blitz-5+0", "blitz-5+3",
		"rapid-10+0", "rapid-10+5", "rapid-15+10", "rapid-30+0",
		"daily-1", "daily-3", "daily-7",
	}
	c := make(PoolCatalog, len(ids))
	for _, id := range ids {
		c[id] = id
	}
	return c
}
