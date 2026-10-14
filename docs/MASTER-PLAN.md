# Competitive Chess Platform — Master Planning Document
### BRD + Delivery Roadmap + Architecture Decisions, consolidated

| Field | Value |
|---|---|
| Document owner | Ashish (Product/Eng lead) |
| Status | v2.1 — FOUNDATION baseline, all architecture + sign-off decisions resolved (only pre-launch legal review outstanding) |
| Last updated | 2026-10-11 |
| Competitors / benchmark | Chess.com, Lichess |
| Constraint | Build maximally before spending; no launch deadline; state-of-the-art tech; team + capital secured |
| Platforms | Web app, iOS app, Android app |
| Supersedes | `chess-platform-brd.md`, `chess-platform-roadmap.md`, `chess-platform-adr.md` (now merged here) |

---

## Table of contents

- **PART I — Business context** — vision, goals, personas, non-functional requirements
- **PART II — Functional requirements** — all 52 modules (F-01…F-52), grouped A–H, + master mapping
- **PART III — Cross-cutting systems** — the 14 systems (X-1…X-14) every feature depends on
- **PART IV — Priority tiering & build sequence** — what to build first, by risk
- **PART V — Delivery roadmap** — 8 parallel streams, 6 dependency gates, phases P0–P5, start-this-week list, tech direction
- **PART VI — Architecture decisions (ADRs)** — firm calls on the architecture-shaping open questions
- **PART VII — Open items & what needs you** — remaining TODOs and the decisions only you own
- **PART VIII — Data model & API contract (v0 direction)** — core entities, game-state wire format, API/WS surface
- **PART IX — Non-functional budgets (measured)** — concrete SLOs, capacity, storage, cost envelope
- **PART X — Test & quality strategy** — engine conformance corpus, test pyramid, release gates
- **PART XI — Security & abuse threat model** — chess-specific threats + controls
- **PART XII — Definition of done & launch bar** — the checklist that defines "quality warrants launch"
- **PART XIII — Operations & resilience** — DR/backup, on-call, rollout, versioning
- **PART XIV — Estimation & staffing** — stream sizing + phase durations
- **Appendix A — Glossary of groups**

ID conventions (stable handles for tickets/specs/tests):
`F-xx` feature module · `NFR-x` non-functional requirement · `X-x` cross-cutting system · `G-x` business goal · `P-x` persona · `S-x` delivery stream · `G-A…G-F` dependency gate · `P0…P5` phase · `ADR-x` architecture decision · `OPEN-x` open item.

---

# PART I — BUSINESS CONTEXT

## 1. Vision

Build a competitive, full-featured online chess platform — web + iOS + Android — matching the breadth of Chess.com and Lichess, built on state-of-the-art technology and infrastructure, launched only when quality warrants it.

## 2. Business goals

| ID | Goal |
|---|---|
| G-1 | Achieve feature parity with Chess.com/Lichess across Play, Puzzles, Learn, Train, Community, Watch |
| G-2 | Deliver a real-time play experience with sub-100ms perceived move latency and provably fair anti-cheat |
| G-3 | Support millions of concurrent users with horizontal scalability from day one of launch |
| G-4 | Establish a rating system credible enough that competitive players trust and defend it |
| G-5 | Build a content/learning funnel (lessons, puzzles, courses) that drives retention and future monetization |

## 3. Target users (personas)

| ID | Persona | Core needs |
|---|---|---|
| P-1 | Casual player | Fast matchmaking, play vs friends/bots, puzzles, mobile-first |
| P-2 | Competitive/rated player | Accurate ratings, anti-cheat, tournaments, deep stats, analysis |
| P-3 | Learner / improver | Lessons, courses, puzzle themes, coach bots, game reports |
| P-4 | Coach | Classroom, student management, create challenges, review games |
| P-5 | Spectator / fan | Watch live events, streams, "playing now", top players |
| P-6 | Club/community organizer | Clubs, team matches, vote chess, leagues, forums |
| P-7 | Child / ChessKid audience | Safe, simplified, moderated experience (F-41) |

## 4. Non-functional requirements (NFRs)

| ID | Requirement | Measured target (full budgets in Part IX) |
|---|---|---|
| NFR-1 | **Move latency** | Move ack p50 < 50 ms, p99 < 150 ms (same-region); authoritative server validation; clock drift < 50 ms over a game |
| NFR-2 | **Availability** | Play services 99.95% monthly (≤ 21.9 min/mo); non-core features 99.5%; zero in-progress-game loss on single-node failure |
| NFR-3 | **Scalability** | Launch target: 1M concurrent connections, 200k concurrent live games, 50k moves/sec peak; linear horizontal scale |
| NFR-4 | **Fair play / anti-cheat** | 100% of rated games scored; flagged-game review SLA < 72 h; false-positive rate < 0.1% of ban actions |
| NFR-5 | **Correctness** | Rules engine passes the full perft corpus + per-variant conformance suite at 100%; 0 known legal-move defects at any gate |
| NFR-6 | **Security** | OWASP ASVS L2; all threats in Part XI have a mapped control; secrets never in code; mTLS between services |
| NFR-7 | **Data integrity** | RPO ≤ 5 s for game events, RTO ≤ 15 min; every move durably persisted before ack; rating changes immutable & auditable |
| NFR-8 | **Accessibility** | WCAG 2.1 AA; full keyboard play; screen-reader move announcements; color-blind-safe board themes |
| NFR-9 | **Observability** | p99 trace coverage on play path; every game fully replayable from the event store; alert-on-SLO-burn |
| NFR-10 | **Platform parity** | Web, iOS, Android on one API (G-D); core-loop feature-equivalent; shared protocol test suite passes on all three |
| NFR-11 | **Internationalization** | i18n-ready from first commit (externalized strings, RTL, locale formats); 0 hard-coded user-facing strings in CI check |
| NFR-12 | **Compliance** | GDPR global baseline; COPPA for F-41; data-subject export/delete within statutory windows; regional data residency as config |

---

# PART II — FUNCTIONAL REQUIREMENTS

## Section A — PLAY

### F-01 Play Online
- **F-01.1** Formats: **Standard**, **Chess960** (Fischer Random).
- **F-01.2** Time controls:
  - **Bullet:** 1 min, 1+1, 2+1, 30 sec, 20 sec + 1
  - **Blitz:** 3 min, 3+2, 5 min, 5+3
  - **Rapid:** 10 min, 10+5, 15+10, 30 min, 20 min, 60 min
  - **Daily (per move):** 1 day, 2 days, 3 days, 5 days, 7 days, 14 days
  - **Custom:** `<min> <sec-base> <increment>`
- **F-01.3** Matchmaking by rating + time control + format; rated/unrated toggle.
- **F-01.4** Live clock sync, draw offers, resign, abort, rematch, takeback (unrated/friendly only).
- **F-01.5** Disconnect handling: reconnect window, clock-aware forfeit.

### F-02 Play Bot
- **F-02.1** Difficulty tiers: New to chess, Beginner, Novice, Intermediate, Intermediate II, Advanced, Expert, Master, Grandmaster, **Adaptive**, **Engine**, **Maximum**.
- **F-02.2** Bot personalities (optional, Chess.com-style named bots) — *see OPEN-3*.
- **F-02.3** Adaptive bot adjusts strength to user's recent performance.
- **F-02.4** Hint / takeback / analysis-during-play affordances for learning context.

### F-03 Play Coach
- **F-03.1** Same difficulty tiers as F-02.
- **F-03.2** Coach gives in-game feedback, suggests better moves, explains mistakes.
- **F-03.3** Ties into Learn (F-14) and Game Report (F-22-GR).

### F-04 Play Friend
- **F-04.1** Challenge a friend by username, challenge link, or in-app.
- **F-04.2** Choose format, time control, color, rated/unrated.
- **F-04.3** Share challenge link (F-22 friend links).

### F-05 Stats
- **F-05.1** Per-format, tracked separately: Bullet / Blitz / Rapid / Daily / each variant.
- **F-05.2** Time windows: **7 days, 30 days, 90 days, 1 year, All Time**.
- **F-05.3** Metrics: Brilliant moves, Great moves, Puzzles, Rating change, Openings, Tactics, Strategy, Endgames, Advanced stats, Awards, Achievements.
- **F-05.4** Social stats: Friends stats & ranking; Clubs stats & rankings.

### F-06 Tournament
- **F-06.1** **Current:** time, tournament type, game time, duration, status, players, Join option.
- **F-06.2** **Schedule** (upcoming tournaments).
- **F-06.3** **Watch** (spectate ongoing).
- **F-06.4** **Create** tournament.
- **F-06.5** Formats: **Arena**, **Swiss**.
- **F-06.6** **Daily tournament** (correspondence-style).
- **F-06.7** **Tournament Directory** (browse/search all).

### F-07 Variant
Supported variants (each needs its own correct rules engine + rating pool):
- Bughouse, Custom Position/Odds, 3-Check, Crazyhouse, King of the Hill, 4-Player Chess, Spell Chess, Fog of War, Atomic, Chaturaji, Giveaway (Antichess), Duck Chess, Setup Chess, Horde, Self-Partnering.
- **F-07.1** Each variant: full rules enforcement, matchmaking, optional separate rating.
- **F-07.2** 4-player & Chaturaji require multi-player board + turn model (distinct from 2-player). *See ADR-3 / OPEN-6.*

### F-08 Game History
- **F-08.1** Buckets: All Games, Favourites, Live, Daily, Bot, Coach.
- **F-08.2** Filters: by date, by accuracy, number of moves, opponent rating.
- **F-08.3** Views: Full View, Compact View.
- **F-08.4** Per-game actions: Classroom, Add to game collection, Analyse, Download (PGN), Show move timestamp, Select one of multiple games (bulk select).
- **F-08.5** Search games:
  - Scope: your games / others' games.
  - By game type: bullet, blitz, rapid, daily, live Chess960, daily Chess960, bughouse, etc.
  - Filter by result; openings; color; match type (tournament, club); newest/oldest; rated/unrated/all.
  - Opponent title filter: All, Titled players, GM, WGM, IM, WIM, FM, WFM, NM, WNM, CM, WCM.
  - Start date–end date; min rating–max rating; favourite games.

## Section B — PUZZLES

**Shared puzzle requirements (apply to all B modules):** comments, share, replay, filter (all puzzles / puzzles I have failed), rating range, my score.

### F-09 Puzzles
- **F-09.1** Core tactics trainer; rated puzzle Elo.
- **F-09.2** Shared features above + solution reveal, retry, next.

### F-10 Daily Puzzles
- **F-10.1** One featured puzzle per day; streak tracking; comments.

### F-11 Puzzle Rush
- **F-11.1** Modes: 3 min, 5 min, **Survival**.
- **F-11.2** Leaderboards: global / friends / personal; windows: all / hourly / daily.
- **F-11.3** Metrics: Best today, Top score, Full history.

### F-12 Puzzle Battle
- **F-12.1** Actions: Play, Watch, Leaderboard.
- **F-12.2** Leaderboard dimensions: Points, Rating × global/friends × season/hourly/daily.
- **F-12.3** Real-time head-to-head puzzle racing.

### F-13 Custom Puzzle
- **F-13.1** Theme filters (full set): All themes, Back Rank Mate, Discovery, Endgame Tactics, Fork, Hanging Piece, Mate in 1, Mate in 2, Mate in 3+, Mating Net, Opposition, Pawn Endgame, Pin, Promotion, Queen Sacrifice, Sacrifice, Scholar's Mate, Skewer, Trapped Piece, Under Promotion.
- **F-13.2** Combine theme + rating range + solved/failed filters.

## Section C — LEARN

### F-14 Lessons
- **F-14.1** Play coach, Learn an opening, Lesson Library, Learn tactics, Learn strategy, Learn endgame, Master games.
- **F-14.2** Content taxonomy & initial catalog — *see OPEN-1*.

### F-15 Openings
- **F-15.1** Opening explorer + trainer; tie to Explorer (F-37) and Games Database (F-34).

## Section D — TRAIN

- **F-16 Courses** — structured multi-lesson curricula.
- **F-17 Analysis** — engine analysis board, eval bar, variations, PGN import/export.
- **F-18 Insights** — aggregate performance analytics across games.
- **F-19 Classroom** — coach-led shared board + student management.
- **F-20 Endgame** — endgame trainer with tablebase integration.
- **F-21 Practice** — drills (openings, tactics, endgames).
- **F-22-GR Game Report** — post-game accuracy, move classification (brilliant/great/blunder), key moments, coach commentary. *(Internal ID avoids clash with Friends F-22.)*

## Section E — COMMUNITY

### F-22 Friends
- Friend Link (share, post, copy link), Find friends, Send email invite, Create challenge link, Friends List, Friend request, Friends leaderboard.

### F-23 Clubs
- Your clubs, Local clubs, Federation, Leagues, Daily matches, Live matches, Vote chess, Create club, Clubs close to you by location, Discover clubs.
- **Members (inline under Clubs):** Titled Players, Recent opponents, Top players, Coaches, Streamers, Friends, Friend Request, Search members, Stats.

- **F-24 Coaches** — coach directory, profiles, booking (future monetization).
- **F-25 Top Players** — ranked list by rating/format.
- **F-26 Chess Ratings** — rating leaderboards & distribution.
- **F-27 Leaderboards** — global/format/variant leaderboards.
- **F-28 Chess Today** — daily digest/landing.
- **F-29 News**
- **F-30 Articles**
- **F-31 Blogs** — user + editorial blogs.
- **F-32 Forums** — threaded discussion, moderation.

> Community "Others" the user flagged to expand later — *see OPEN-2*.

## Section F — OTHERS

| ID | Module | Note |
|---|---|---|
| F-33 | Collections | Curated game collections, user-created |
| F-34 | Games Database | Master/user game archive, searchable |
| F-35 | Chess Terms | Glossary/encyclopedia |
| F-36 | Rules | Rules reference per variant |
| F-37 | Explorer | Opening explorer (DB-backed) |
| F-38 | Vote Chess | Community votes on moves |
| F-39 | Solo Chess | Single-player clear-the-board mode |
| F-40 | Computer Championship | Engine-vs-engine events + viewer |
| F-41 | ChessKid | Child-safe sub-platform — **COPPA critical (NFR-12)** |
| F-42 | Tools | Misc utilities (board editor, PGN tools) — *define, OPEN-7* |
| F-43 | Vision | Board-vision trainer (name squares/coordinates) |
| F-44 | Shop / Merch | E-commerce — *scope later, OPEN-8* |
| F-45 | Gift | Gift memberships/items |

## Section G — MANAGEMENT

| ID | Module | Note |
|---|---|---|
| F-46 | Player Profile | Avatar, bio, ratings, badges, country, stats hub |
| F-47 | Messages | Private messaging / inbox |
| F-48 | Chat | In-game chat + global/club chat (moderated) |
| F-49 | Notifications | Push (mobile) + in-app + email prefs |

## Section H — WATCH

| ID | Module | Note |
|---|---|---|
| F-50 | Events | Live tournament/event hub |
| F-51 | Stream | Streaming integration / embedded broadcast |
| F-52 | Playing Now | Live games browser, follow top boards |

## Master module mapping (your intake list → requirement IDs)

| # | Module | ID |
|---|---|---|
| 1 | Play online | F-01 |
| 2 | Play Bot | F-02 |
| 3 | Play Coach | F-03 |
| 4 | Play friend | F-04 |
| 5 | Stats | F-05 |
| 6 | Tournament | F-06 |
| 7 | Variant | F-07 |
| 8 | Game History | F-08 |
| 9 | Puzzles | F-09 |
| 10 | Daily Puzzles | F-10 |
| 11 | Puzzle Rush | F-11 |
| 12 | Puzzle Battle | F-12 |
| 13 | Custom Puzzle | F-13 |
| 14 | Lessons | F-14 |
| 15 | Openings | F-15 |
| 16 | Courses | F-16 |
| 17 | Analysis | F-17 |
| 18 | Insights | F-18 |
| 19 | Classroom | F-19 |
| 20 | Endgame | F-20 |
| 21 | Practice | F-21 |
| 22 | Game Report | F-22-GR |
| 23 | Friends | F-22 |
| 24 | Clubs | F-23 |
| 25 | Coaches | F-24 |
| 26 | Top Players | F-25 |
| 27 | Chess Ratings | F-26 |
| 28 | Leaderboards | F-27 |
| 29 | Chess Today | F-28 |
| 30 | News | F-29 |
| 31 | Articles | F-30 |
| 32 | Blogs | F-31 |
| 33 | Forums | F-32 |
| 34 | Collections | F-33 |
| 35 | Games Database | F-34 |
| 36 | Chess Terms | F-35 |
| 37 | Rules | F-36 |
| 38 | Explorer | F-37 |
| 39 | Vote chess | F-38 |
| 40 | Solo Chess | F-39 |
| 41 | Computer championship | F-40 |
| 42 | ChessKid | F-41 |
| 43 | Tools | F-42 |
| 44 | Vision | F-43 |
| 45 | Shop / Merch | F-44 |
| 46 | Gift | F-45 |
| 47 | Player Profile | F-46 |
| 48 | Messages | F-47 |
| 49 | Chat | F-48 |
| 50 | Notifications | F-49 |
| 51 | Event | F-50 |
| 52 | Stream | F-51 |
| 53 | Playing Now | F-52 |

> **Numbering reconciliation (resolved — see ADR-5):** your intake had two "item 4"s (Play friend + Stats) and section counts didn't sum cleanly. This clean 1–52 mapping is now canonical; all tickets reference F-xx IDs.

---

# PART III — CROSS-CUTTING SYSTEMS

Not in your feature list, but **every** feature depends on them. These are the actual hard engineering and the first things to build.

| ID | System | Why it's foundational |
|---|---|---|
| X-1 | **Chess rules engine** (all variants) | Every Play/Puzzle/Analysis/Variant feature sits on correct, fast, server-authoritative move generation & validation (NFR-5). Highest-risk component. |
| X-2 | **Real-time game server** | Clock management, move relay, matchmaking, reconnection, spectating. Powers F-01, F-04, F-06, F-11, F-12, F-50–52. |
| X-3 | **Accounts & identity** | Signup/login (OAuth + email), profiles (F-46), sessions, device management, COPPA for F-41. |
| X-4 | **Rating system** | Glicko-2 per-pool (ADR-1); drives F-05, F-25–27, matchmaking. |
| X-5 | **Anti-cheat / fair play** | Engine-correlation + behavioral analysis + review tooling (ADR-2). Competitive credibility (G-2, NFR-4). |
| X-6 | **Analysis engine integration** | Stockfish/NNUE + tablebases; powers F-17, F-20, F-21, F-22-GR, bots (F-02/03). |
| X-7 | **Puzzle generation pipeline** | Generate/curate puzzles with themes & ratings from game DB (all of Section B). |
| X-8 | **Content/CMS** | Lessons, courses, news, articles, blogs (F-14–16, F-29–31). |
| X-9 | **Notifications infra** | Push (APNs/FCM), email, in-app (F-49). |
| X-10 | **Media/streaming** | Broadcast + spectate pipeline for Watch (F-50–52). |
| X-11 | **Moderation & trust/safety** | Chat/forums/clubs moderation, reporting, bans (F-32, F-47, F-48, NFR-12). |
| X-12 | **Payments & entitlements** | Memberships, shop, gifts (F-44, F-45) — build the hook early (ADR-8). |
| X-13 | **Shared API + client SDK** | One backend contract across web/iOS/Android (NFR-10). |
| X-14 | **Observability & data platform** | Metrics, tracing, game-event store, analytics warehouse (NFR-9, feeds F-18). |

---

# PART IV — PRIORITY TIERING & BUILD SEQUENCE

Build order by **risk and dependency**, not list order.

### Tier 0 — Prove the hard core (highest risk, first)
- X-1 Rules engine (Standard, 100% correct) + X-2 real-time server + X-3 accounts + X-4 ratings.
- **F-01 Play Online (Standard only)** end-to-end on web.
- X-5 Anti-cheat foundations wired in from the start.
- *Why first:* if real-time correct, fair, scalable play doesn't work, nothing else matters.

### Tier 1 — Core loop & retention
- F-02 Play Bot, F-04 Play Friend, F-08 Game History, F-17 Analysis, F-22-GR Game Report.
- F-09 Puzzles + F-13 Custom Puzzle (needs X-7).
- F-46 Profile, F-48 Chat, F-49 Notifications.
- iOS + Android reach parity on this loop (X-13).

### Tier 2 — Competitive & social depth
- F-06 Tournaments (Arena+Swiss), F-05 Stats, F-25–27 ratings/leaderboards.
- F-07 Variants (incrementally; Chess960 first).
- F-10–12 Daily Puzzles / Rush / Battle. F-22 Friends, F-23 Clubs.

### Tier 3 — Learn/Train content platform
- F-14–16 Lessons/Openings/Courses, F-19 Classroom, F-20 Endgame, F-21 Practice, F-03 Play Coach, F-18 Insights. X-8 CMS matures.

### Tier 4 — Breadth & ecosystem
- F-28–32 News/Articles/Blogs/Forums, F-33–43 Others, F-50–52 Watch, F-41 ChessKid, F-44–45 Shop/Gift.

> **Guiding rule:** breadth (46 of 52 modules) is the retention/SEO/ecosystem layer. Make-or-break is Tier 0–1. Spend the "no money yet" window there.

---

# PART V — DELIVERY ROADMAP

A large team runs **multiple squads in parallel**, synchronized by a small set of **hard dependency gates**. The art is making sure no squad is blocked.

> **The one rule that saves the most money:** nothing ships to a rated environment until **G-A (correct engine)**, **G-B (fair-play baseline)**, and **G-C (rating integrity)** are green.

## V.1 Team structure (streams)

| Stream | Owns | Why it exists |
|---|---|---|
| **S1 · Core Chess Engine** | X-1 rules engine (all variants), X-6 analysis, X-7 puzzle pipeline | Correctness heart. Highest-skill, highest-risk. |
| **S2 · Real-time Play Platform** | X-2 game server, matchmaking, clocks, reconnection, F-01, F-04, F-06, F-07 | Live experience + scale. |
| **S3 · Platform/Backend Services** | X-3 accounts, X-4 ratings, X-13 API, X-14 data, F-05, F-08, F-25–27, F-46 | Services spine. |
| **S4 · Web Client** | Web app, all features | Reference client. |
| **S5 · Mobile Clients** | iOS + Android | Platform parity (NFR-10). |
| **S6 · Trust & Safety** | X-5 anti-cheat, X-11 moderation, F-32/47/48 safety, F-41 compliance | Credibility + legal. |
| **S7 · Learn/Train/Content** | X-8 CMS, F-14–21, F-28–37, F-42–43 | Retention/education. |
| **S8 · Community/Watch/Commerce** | F-22–24, F-33, F-38–40, F-44–45, F-49–52, X-9/X-10/X-12 | Social graph, spectating, monetization hooks. |

Plus a central **Architecture/Platform-Eng** group owning infra (X-14, CI/CD, environments, API contract X-13).

## V.2 Dependency gates

| Gate | Condition to pass | Blocks until passed |
|---|---|---|
| **G-A · Engine correct** | X-1 Standard rules 100% correct vs perft/FEN corpus; sub-ms move gen | S2 live play, S1 variants, all puzzle/analysis work |
| **G-B · Fair-play baseline** | X-5 can flag engine-correlated play + review queue | Any *rated* play going live |
| **G-C · Rating integrity** | X-4 Glicko-2 deterministic, auditable, per-format pools | F-05 stats, F-25–27 leaderboards, rating matchmaking |
| **G-D · API contract frozen (v1)** | X-13 API spec stable for clients | S5 mobile parity work at scale |
| **G-E · Scale proven** | X-2 load-tested to target concurrency (NFR-3) with chaos/reconnect | Public beta |
| **G-F · Compliance cleared** | GDPR + COPPA (F-41) + residency reviewed (NFR-12) | ChessKid + any public launch |

## V.3 Phases

**P0 — Foundations & spikes** *(all streams active)* — de-risk the killers, set up the factory.

| Stream | P0 work |
|---|---|
| S1 | Standard rules engine; perft test corpus; NNUE/Stockfish integration spike |
| S2 | Game-server architecture spike; clock model; real-time transport choice; matchmaking design |
| S3 | Accounts/auth (OAuth + email), profile data model, rating service (ADR-1), API gateway, event store |
| S4 | Web shell, design system, board component (render + legal-move UX from S1) |
| S5 | Mobile shells (iOS/Android), shared networking layer, board component |
| S6 | Anti-cheat architecture (ADR-2); signal collection from day 1 |
| S7 | CMS selection/spike; content taxonomy (OPEN-1) |
| S8 | Social graph model; notifications infra (APNs/FCM); entitlements hook design (ADR-8) |
| Platform-Eng | CI/CD, dev/stage environments, observability (NFR-9), load-test harness, freeze API v0 |

*Exit:* **G-A** green. API v0 drafted. All shells render a live board.

**P1 — Core play loop (web first)** — a human plays a correct, fair, rated Standard game end to end on web.
- S2+S1: F-01 (Standard), all time controls, reconnection, draw/resign/rematch.
- S3: F-05 (basic), F-08 + PGN, ratings live. S6: **G-B** wired in + review queue.
- S4: full play UI, clock, review entry. S1: F-17 + F-22-GR.
- S7: F-09 + F-13 (needs X-7). S8: F-46, F-48, F-49.
- *Exit:* **G-B** + **G-C** green. Internal dogfood.

**P2 — Mobile parity + core depth** — core loop on all three platforms + competitive features.
- **G-D** frozen → S5 brings iOS+Android to parity.
- S2: F-02, F-04 + challenge links, F-07 Chess960, F-06 Tournaments (Arena+Swiss).
- S3: F-25–27, F-05 full. S7: F-10, F-11, F-20, F-21. S8: F-22 Friends, mobile push.

**P3 — Variants, real-time competition, social** — breadth begins.
- S1+S2: F-07 rollout (Crazyhouse, Atomic, KotH, 3-Check, Horde, Giveaway first; 4-Player/Chaturaji separate track — ADR-3).
- S7/S8: F-12 Puzzle Battle, F-11 leaderboards. S8: F-23 Clubs, F-38 Vote Chess.
- S1: F-03 Play Coach, F-18 Insights, F-19 Classroom.

**P4 — Learn/Train platform + ecosystem.**
- S7: F-14–16, F-37, F-34, F-35, F-36, F-43.
- S8: F-28–32 (+ X-11 moderation), F-33, F-39, F-40, F-50–52 (X-10 media).

**P5 — ChessKid, commerce, launch hardening.**
- S6: **G-F** compliance (COPPA/GDPR/residency). S8: F-44, F-45, X-12 live; monetization decided (ADR-8).
- S2/Platform-Eng: **G-E** scale proven. All: hardening, accessibility (NFR-8), i18n locales (OPEN-12), security review (NFR-6).
- *Exit:* Public beta → launch when quality warrants (your call, no deadline).

## V.4 What starts THIS week (parallel — nobody waits)

1. **S1:** Standard rules engine + perft corpus. *(Critical path to G-A — staff heaviest.)*
2. **S2:** Game-server + clock + transport spike; two clients moving pieces live.
3. **S3:** Accounts/auth + profile model; stand up rating service (ADR-1).
4. **S6:** Start collecting anti-cheat signals (ADR-2); confirm investment level with Ashish.
5. **Platform-Eng:** CI/CD, environments, observability, load-test harness, freeze API v0 (X-13).
6. **S4/S5:** App shells + shared board component (fed by S1).
7. **S7/S8:** CMS + social-graph + notifications design spikes.

## V.5 Suggested tech direction *(advisory — confirm with architecture group)*

| Concern | Direction |
|---|---|
| Rules/engine core | Rust or C++ for move gen & validation; Stockfish/NNUE for analysis |
| Real-time server | Go or Rust, WebSocket + binary protocol; sharded per-game state; Redis for ephemeral clock/matchmaking |
| Backend services | Polyglot microservices behind one API gateway; gRPC internal, REST/WS external |
| Datastore | Postgres (accounts/ratings/relational) + append-only event store for games + object storage for PGN/media |
| Rating | Glicko-2 (ADR-1) |
| Web | TypeScript + React (or Svelte); shared board lib |
| Mobile | Native (Swift/Kotlin) or shared core (KMP/Flutter) — **decision needed** |
| Infra | Kubernetes, IaC, multi-region ready; CDN for static/puzzles |
| Observability | OpenTelemetry traces/metrics/logs; replayable game events |

## V.6 Gate checklist

- [ ] **G-A** Engine correct (perft green, all legal-move edge cases)
- [ ] **G-B** Fair-play baseline + review queue before any rated play
- [ ] **G-C** Ratings deterministic & auditable before leaderboards/matchmaking
- [ ] **G-D** API v1 frozen before mobile parity push
- [ ] **G-E** Scale proven before public beta
- [ ] **G-F** COPPA/GDPR/residency cleared before ChessKid + launch

---

# PART VI — ARCHITECTURE DECISIONS (ADRs)

Firm calls on the architecture-shaping open questions. **DECIDED** = recommended default, proceed unless vetoed. **NEEDS SIGN-OFF** = business/legal/money, only Ashish owns it.

## ADR-1 (OPEN-4) · Rating system — **DECIDED: Glicko-2, per-pool**
Use **Glicko-2** with a **separate pool per (format × variant)**; store rating, RD, volatility per pool per user; every rating change is an immutable event tied to the game ID (deterministic, auditable → G-C).
- *Why:* proven at scale (Lichess), models uncertainty + volatility (better matchmaking + anti-cheat signal); per-pool separation is required for credibility.
- *Rejected:* classic Elo (no uncertainty), Glicko-1 (superseded), custom/ML (un-auditable, erodes trust), TrueSkill (team-game fit).
- *Unblocks:* G-C, F-05, F-25–27, matchmaking. Build as standalone event-sourced service in S3.
- *Defer (tunables, not architecture):* starting rating, provisional count, RD floor/ceiling, rating-period length.

## ADR-2 (OPEN-5) · Anti-cheat — **DECIDED: build in-house, phased; fair-play is a permanent standing function**
**Build fair-play in-house**, phased, collecting signals from **day one**. Append-only behavioral event stream per game/user → async detection models + human review queue with case tooling. No off-the-shelf vendor as core (none fit chess). **Signed off:** fair-play is committed as a **permanent cost center** (data scientists + review staff + appeals ops), not a one-time build — S6 is a standing team that grows with the user base.
- *Phases:* (1) P0–P1 collect signals (move times, centipawn-loss, engine-match %, device/session fingerprint, focus/blur, premove patterns, rating volatility); (2) P1/G-B baseline engine-correlation + outlier flags → review queue (not auto-ban); (3) P2+ ML classifier + appeals + graduated responses.
- *Why:* fair play is what lets you compete for serious players; signals must be captured from the first game; detection is adversarial IP needing deep game-server integration.
- *Rejected:* vendor-as-core (black-box, can't see server timing); defer to post-launch (fatal — no signal history, no credibility, costly retrofit).
- **Signed off (2026-10-11):** fair-play is a standing function; S6 staffs DS + review + appeals permanently.
- *Unblocks:* G-B, all rated play. S6 owns; signal hooks land in S2 game server at P0.

## ADR-3 (OPEN-6) · Multi-player board model — **DECIDED: generalized game container now, N-player engine P3**
One **generalized game model** at the data/server layer (game = N seats, turn order, per-seat clock, result vector) built in P0; the **4-Player/Chaturaji rules engine** is a distinct S1 module built in **P3**, off the Tier-0 critical path. 2-player ships first (G-A).
- *Why:* N-player has a different board/turn/scoring/clock model — generalize the container cheaply now (vector result, not win/draw/loss enum); but the N-player move-gen shares little with 2-player, so build it later without blocking Standard chess.
- *Rejected:* hard-code 2-player (forces a rewrite); build N-player in Tier 0 (delays G-A).

## ADR-4 (OPEN-11) · Variant rules — **DECIDED: pin to Lichess/FIDE, data-driven config; Spell Chess ruleset defined**
Each variant pinned to an authoritative definition (default Lichess/FIDE), encoded as **data-driven variant config** over a core move-generator + per-variant overlay (not scattered branches).
- *Pinned defaults (frozen):* Crazyhouse/Bughouse, Atomic, King of the Hill, 3-Check, Horde, Giveaway/Antichess per Lichess; Chaturaji = 4-player points (ADR-3); Setup/Self-Partnering/Custom Position/Odds = setup modes feeding the Standard engine.
- **Fog of War — DECIDED: standard.** Lichess Dark-chess rules: a player sees only their own pieces and the squares their pieces can legally move to/attack; no check or checkmate announcement; **win by capturing the enemy king**. Fog is applied server-side per viewer — the client is never sent hidden squares (also a T-1/anti-cheat property).
- **Duck Chess — DECIDED: standard.** After every move the mover must place/move **the duck** onto any empty square; the duck is an un-capturable blocker that occupies a square and blocks all movement/line pieces through it; there is **no check** (the king can be left "in check"); **win by capturing the enemy king**. The duck move is part of the ply and is recorded in the event log.
- **Spell Chess — DECIDED: ruleset designed below (v1).**

### Spell Chess ruleset (v1 — designed for fun + implementability)
> Design constraints honored: **deterministic & server-authoritative** (no hidden RNG the client could predict or the server couldn't audit), **anti-cheat-friendly** (every spell cast is an event in the log), **bounded** (spells can't make the engine non-terminating or illegal-state-prone), and **learnable** (a small, memorable set).

**Core idea:** normal chess, but each player has **Mana** and a hand of **Spells** they can cast instead of, or in addition to, a move. Mana is earned by playing; spells bend the rules in flashy but bounded ways. The king can never be removed by a spell — checkmate still ends the game normally.

**Mana:**
- Both players start at **0 mana**, cap **10**.
- +1 mana at the start of each of your turns; **+1 bonus** when you capture a piece (so aggression fuels magic).
- Casting a spell costs mana (below). You may cast **at most one spell per turn**, either *before* your move (then you still move) or *as* your turn (some spells end the turn).

**The spell book (v1 — 8 spells, balanced + fun):**
| Spell | Cost | Effect | Guardrails |
|---|---|---|---|
| **Teleport** | 4 | Move one of your non-king pieces to any empty square (ignores movement rules). Then your turn ends. | Cannot give check-from-nowhere that is mate on the same ply unless it would be a legal check; cannot capture. |
| **Freeze** | 3 | Pick one enemy piece; it cannot move next turn (frozen). | Cannot freeze the king. Lasts one enemy turn. |
| **Shield** | 3 | One of your pieces becomes un-capturable for one full round. | Shielded piece still blocks/checks normally; king may be shielded (but check rules still apply via other pieces). |
| **Summon Pawn** | 5 | Place a pawn of your color on any empty square on ranks 2–6 (your half +1). | Not on 1st/8th; counts toward normal promotion rules thereafter. |
| **Swap** | 5 | Swap the squares of two of **your** pieces. Turn ends. | Not the king if it would land in check; no swap into an occupied square. |
| **Bolt** | 6 | Destroy (remove) one enemy **non-king** piece adjacent to any of your pieces. Turn ends. | Never targets the king; "adjacent" = king-move distance. |
| **Haste** | 4 | After your normal move, make a **second** move with the **same** piece. | The piece must have a legal second move; no castling as the second move; cannot be used to make two captures that expose your own king. |
| **Polymorph** | 7 | Turn one of your minor pieces (N/B) into the other type (N↔B), or a rook into a knight. | One-way per cast; never affects king/queen/pawn; cosmetic + tactical. |

**Win conditions:** standard checkmate, resignation, or flag. All normal end conditions (stalemate, threefold, 50-move) still apply; spell casts **reset the 50-move counter like a pawn move/capture** (prevents infinite stalling via spells).

**Legality & audit:** a spell cast is an event `SpellCast {spell, params, mana_before, mana_after}` in the game stream; the server validates mana, target legality, and that the resulting position is legal (your king not left in check unless rules allow). Fully replayable and anti-cheat-visible.

**Rating:** its own pool (ADR-1). Flagged **experimental v1** — balance tunables (costs, cap, regen) are config, expected to be rebalanced from real play data.

- *Why (overall):* variants multiply correctness risk (NFR-5); data-driven config + per-variant test corpus keeps the engine maintainable; S1 adds variants incrementally in P3. Spell Chess is deliberately a bounded rule-overlay on the Standard engine, so it reuses move-gen rather than forking it.

## ADR-5 (OPEN-9) · Module numbering — **DECIDED: adopt the 1–52 master mapping**
The clean 1–52 mapping in Part II is canonical. Intake had two "item 4"s; this reconciles it. All tickets/specs/tests reference **F-xx IDs**, never raw intake numbers. Zero code impact; prevents cross-team churn.

## ADR-6 (OPEN-13) · Compliance & data residency — **DECIDED: global launch, GDPR baseline, COPPA-as-needed; legal confirms specifics**
Target launch is **global**, so design for **GDPR-grade protection worldwide** (one code path), **COPPA controls applied as needed** (under-13 / ChessKid F-41 and any jurisdiction requiring it), and a **multi-region-capable data layer** (region tag + routable storage) so residency is deployment config, not surgery.
- *Why:* global reach means the strictest applicable regime governs each user; GDPR-as-baseline + per-region overlays is the only sane architecture. COPPA (under-13 verifiable parental consent, restricted collection, no behavioral ads) is legally serious and gates F-41.
- **Signed off (2026-10-11):** regions = global; COPPA applied as needed; ChessKid (F-41) ships with full COPPA controls. **Legal must still confirm specifics** per major market (EU/UK GDPR, US COPPA/CCPA, India DPDP, Brazil LGPD, …) and sign the launch-region compliance checklist before G-F — encode now, lawyer-confirms-before-launch.

## ADR-7 (OPEN-12) · i18n — **DECIDED: i18n-ready at P0; locale list deferred**
Build i18n-ready from the first line (externalized strings, locale-aware formatting, RTL support, no concatenated sentences) — **P0 architecture**. The *launch locale list* is a content/business decision, decidable near P4/P5, that does **not** block the build.
- *Why:* retrofitting i18n is one of the most expensive refactors; doing it upfront costs ~5% discipline.
- *Recommended set (defer confirm):* English first; then Spanish, Russian, Hindi, Portuguese, German, French; Arabic for RTL validation.

## ADR-8 (OPEN-10) · Monetization — **DECIDED: freemium membership; entitlements hook at P0**
**Freemium membership** is the chosen model: free play + rated games + core puzzles for everyone; paid tiers unlock analysis depth, lessons/courses, insights, extra puzzle features, and no-ads. Build the **entitlements/payments hook at P0** (feature gating by plan, wired to feature flags, server-verified) so features gate cleanly across all 52 modules.
- *Why:* ads monetize poorly per-user and alienate the serious players you're courting; membership funds both Chess.com and Lichess-style models. "Money is no concern to build" means you launch clean and monetize on strength. Server-verified entitlements also close threat T-11 (entitlement forgery).
- **Signed off (2026-10-11):** freemium. Tier boundaries (what's free vs paid, price points) are a later product decision — the *architecture* (entitlement capability model) is settled now.

## ADR-9 (deferred) · Mobile stack — **DECIDED: native (Swift + Kotlin) with a shared Rust core**
Build **native UI** (SwiftUI/UIKit on iOS, Jetpack Compose on Android) over a **shared Rust core library** (via UniFFI) that holds the board/rules client logic, move encoding, clock, and protocol client — the same Rust engine S1 builds for the server, compiled for mobile.
- *Why:* chess is latency- and animation-sensitive; native gives the best board feel, gesture handling, and OS integration (push, widgets, deep links). The shared Rust core kills the biggest native cost — duplicated, drift-prone game logic — by reusing the *one* authoritative engine on both platforms and the server. One rules implementation, three consumers.
- *Rejected:* Flutter/KMP-UI (board rendering & gesture latency compromises, weaker native feel for a flagship app); fully separate native codebases (logic drifts, triples the rules-bug surface — the exact NFR-5 risk); web-view wrapper (unacceptable for competitive play).
- *Unblocks:* S5 architecture, G-D. Cost: Rust-FFI tooling investment up front (worth it given S1 is already Rust).

## ADR-10 (new, foundation) · Datastore & transport — **DECIDED: Postgres + event store + Redis; WebSocket binary protocol**
- **Game truth = append-only event store** (every move/clock/result as an immutable event; current state is a fold). Game replay, anti-cheat signals, and audit all derive from this. Durable write **before** move ack (NFR-7).
- **Relational = PostgreSQL** for accounts, ratings, clubs, social graph, content metadata. One primary + read replicas per region.
- **Ephemeral/hot = Redis** for matchmaking pools, live clocks, presence, rate-limit counters, transient leaderboard windows. Never the source of truth.
- **Object storage** (S3-compatible) for PGN exports, puzzle packs, media, course assets; fronted by CDN.
- **Analytics = columnar warehouse** (e.g. ClickHouse/BigQuery) fed from the event stream for F-18 Insights and ops metrics — never queried on the play path.
- **Transport = WebSocket with a compact binary frame** for live play (not JSON — move frames must be tiny and fast); REST/JSON for everything non-realtime; gRPC between internal services.
- *Why:* event-sourcing is the natural fit for chess (a game *is* an ordered event log) and gives replay/audit/anti-cheat for free; separating hot (Redis) / truth (events) / relational (PG) / analytics (columnar) keeps the play path fast and the query path rich without one store doing everything badly.
- *Unblocks:* X-2, X-4, X-14, G-E; defines the storage contract every stream builds against.

## ADR-11 (delegated) · Engine / shared-core language — **DECIDED: Rust**
The chess engine (X-1) and the shared client core (ADR-9) are written in **Rust**. Decision delegated by the owner to Kiro and recorded here.
- *Why:* memory safety without GC (no stop-the-world pause during a live game), C-comparable move-generation performance, and first-class FFI so the *same* engine binds into iOS (Swift) and Android (Kotlin) via UniFFI and compiles to the server — one authoritative rules implementation, three consumers (closes the NFR-5 drift risk). Strong test tooling for the perft corpus (G-A); reference crates exist (`shakmaty`, `cozy-chess`).
- *Rejected:* C++ (max perf but unsafe + painful mobile FFI reuse, no memory-safety guarantee); Go (GC pauses unacceptable on the hot move path); any GC language for the engine.
- *Unblocks:* S1 start, G-A. Build order: engine first.

## ADR-12 (delegated) · Backend service language — **DECIDED: Go for services, Rust for engine/core**
Backend **services** (game-server X-2, accounts/rating X-3/X-4, anti-cheat X-5, puzzles/content X-7/X-8) are written in **Go**; the engine/core stays Rust (ADR-11). Delegated to Kiro, recorded here.
- *Why:* Go gives fast iteration, excellent concurrency for the real-time/WebSocket tier (goroutines map naturally to many concurrent live games), static-binary deployment, and a large hiring pool — ideal where throughput + developer velocity beat raw single-thread perf. The one perf- and correctness-critical component (the engine) stays Rust and is consumed by Go services across a thin FFI/gRPC boundary or sidecar.
- *Rejected:* Rust everywhere (steeper, slower iteration for orchestration/CRUD services, smaller hiring pool — not worth it outside the engine); Node/JS for the real-time tier (GC + single-thread limits at IX.2 scale).
- *Boundary rule:* a service NEVER reimplements chess logic — it calls the Rust engine. Rules logic in a Go service is a bug (violates NFR-5 single-source-of-truth).
- *Unblocks:* S2/S3/S6/S7 service scaffolding.

## ADR-14 (new) · Wire payload codec — **DECIDED: one schema-defined binary codec in the shared Rust core**

> **ID note:** this is **ADR-14, not ADR-13.** ADR-13 is reserved for the owner's held *magic-bitboards skip* decision (drafted, see `engine/PERF.md` / decision D5), which is not yet written into this doc. The codec ADR takes the next free number to avoid an ID collision (steering: never redefine an ID). **DECIDED by the owner 2026-10-12.**

**Context.** ADR-10 fixed "WebSocket **binary** frame" for live play but not *which* binary encoding. SPEC-S2-PROTOCOL §3.1 defines the frame envelope (`ver | type | payload`) and leaves the payload codec as its open item Q-P2. The codec is a shared contract: web (TS), iOS (Swift), Android (Kotlin) and the Go server must all encode/decode the *same* bytes (NFR-10), and a per-platform hand-rolled codec is a drift risk (NFR-5 spirit — one encoding, many consumers).

**Decision.** Use **one schema-defined binary codec compiled into the shared Rust core (ADR-9/11)**, exposed as `encode(msg)->bytes` / `decode(bytes)->msg` and bound into all three clients via UniFFI; the Go server calls the same codec across the ADR-12 FFI/gRPC boundary rather than reimplementing it. Candidate codecs to pick between at RATIFIED (a bounded, reversible sub-choice): a compact self-describing schema codec (`postcard`/`bincode`-family from the Rust ecosystem) vs a cross-language IDL codec (Protobuf/FlatBuffers). Leaning `postcard`-family: smallest frames, zero external IDL toolchain, and it lives natively in the Rust core we already ship — the clients get it for free through the existing FFI, with no second build system.

**Why.**
- **One codec, three clients (NFR-10).** The engine-FFI parity discipline already proved byte-identical behavior across iOS+Android from one Rust core; the wire codec rides the same path, so parity is structural, not tested-in-after-the-fact.
- **Tiny hot-path frames (NFR-1).** A move frame is `type + game_id + ply + uci + clock` — a compact binary codec keeps it to a few dozen bytes, inside the §IX.1 intent→ack budget.
- **No codec drift (NFR-5 spirit).** The only legal encoder/decoder is the shared core; a client or service hand-rolling bytes is a bug, mirroring the "one rules engine" rule.

**Rejected.**
- **JSON on the hot path** — fails ADR-10's "not JSON — move frames must be tiny and fast".
- **Per-platform hand-rolled binary** — three codecs to keep byte-identical by hand; guaranteed drift at some point (NFR-10 risk).
- **Protobuf/FlatBuffers as the default** — viable and kept as the RATIFIED alternative, but adds an IDL toolchain + generated code in three languages when the Rust core can already serve all three through FFI; chosen against unless a concrete need (e.g. a non-FFI consumer) appears.

**Scope / boundary.** This ADR fixes *that there is one codec in the core*; the exact codec library and the byte-level field layout are pinned in SPEC-S2-PROTOCOL §4.3 (its byte-value appendix) at RATIFIED. REST/JSON (non-realtime) is unaffected — it stays JSON (SPEC-PE-API-CONTRACT).

**Unblocks.** SPEC-S2-PROTOCOL RATIFIED (its Q-P2), the §4.3 byte appendix, and the shared-core codec implementation. **DECIDED by the owner 2026-10-12.**

## ADR-15 (new) · Go↔Rust boundary — **DECIDED: one uniform in-process cgo/staticlib C-ABI, via a sibling wrapper crate per Rust crate Go consumes**

> Refines ADR-12's "thin FFI/gRPC boundary or sidecar" into a single concrete, uniform mechanism for every Rust crate the Go tier consumes. **DECIDED by the architecture group, 2026-10-13.** (Reviewed alongside SPEC-S2-GAME-SERVER + PLAN-S2-CODEC v1.1.)

**Context.** ADR-12 put the Go services on a "thin FFI/gRPC boundary or sidecar" to the Rust engine/core but did not fix *which* boundary. Two Rust crates now need Go consumption on the per-move hot path: the **codec** (`pgc-protocol`, every move = decode-intent → encode-event) and the **engine** (`pgc-engine`, server-side legality — NFR-5 forbids reimplementing rules in Go). PLAN-S2-CODEC v1.1 already DECIDED in-process cgo over a sibling `codec-cgo` staticlib for the codec. The engine has only a UniFFI (mobile) seam today and no Go path. Left undecided, the two seams would be designed independently → two buffer-ownership conventions, two `*_free` idioms, two cbindgen/build setups, two fuzz harnesses, in the two most safety-critical FFI boundaries in the system.

**Decision.** Every Rust crate the Go tier consumes gets a **thin, delegate-only sibling C-ABI crate** (`<crate>-cgo`: `crate-type=["staticlib","rlib"]`, `#[no_mangle] extern "C"`, a `cbindgen`-generated header), consumed **in-process via cgo**. The pure crate stays `#![forbid(unsafe_code)]`; the one unavoidable `unsafe` seam is quarantined in the `-cgo` sibling. One uniform convention across all of them:
- **Byte-oriented surface** (bytes in / bytes out) plus a small set of typed scalar accessors for the few fields the caller acts on — smallest unsafe surface, matches the wasm/mobile sibling shape (parity by construction).
- **Buffer ownership:** Rust allocates outputs; Go copies into Go memory and calls the crate's `*_free`; Go never frees a Rust pointer nor hands Rust a retained Go pointer (the cgo pointer rule).
- **Shared build discipline:** a `go:generate`/Makefile step cross-compiles the staticlib per deploy target (linux/amd64, linux/arm64) with the matching Rust `--target`; each `-cgo` crate ships a fuzz + leak test.
This covers `pgc-protocol` → `codec-cgo` (PLAN-S2-CODEC v1.1, building now) and `pgc-engine` → `engine-cgo` (built when the game-server wires move validation; same pattern, not reinvented).

**Why.**
- **One pattern, audited once (quality).** The two most dangerous FFI boundaries share one buffer-ownership contract, one fuzz/leak discipline, one build path — far cheaper to review and maintain than two bespoke setups.
- **Hot-path fit (NFR-1).** In-process cgo is ~tens of ns/call, negligible against the move budget; a sidecar/gRPC hop on the per-move path is backwards (PLAN-S2-CODEC §4.5 rationale).
- **Core stays pristine (NFR-5, G-A).** The pure codec and engine keep `forbid-unsafe` and their dependency-light build; the ABI machinery lives only in the siblings.

**Rejected.**
- **cgo directly against the UniFFI staticlib** — its C surface is shaped for UniFFI's runtime, not plain cgo; wrong tool.
- **gRPC/sidecar for codec or engine** — adds a network hop + failure mode to the operation that must be tiny and reliable (the move path). Sidecar stays the right call only for independently-scaled, own-lifecycle workers (e.g. a heavy analysis/NNUE service) — unchanged from ADR-12.
- **A C-ABI inside the pure crates** — would relax `forbid-unsafe` on the single-source codec/engine; rejected in PLAN-S2-CODEC v1.1 for the codec, generalized here.

**Scope / boundary.** This ADR fixes the *mechanism and its uniformity*. The per-crate C-ABI shape is pinned in each crate's owning spec (codec: PLAN-S2-CODEC v1.1 §4.5; engine: a later ENGINE-CGO note in SPEC-S1-ENGINE-CORE or SPEC-S2-GAME-SERVER §4 when move validation is wired). Does not change any wire/REST contract.

**Unblocks.** SPEC-S2-GAME-SERVER §4 (Q-GS4) and the engine's Go consumption. **DECIDED by the architecture group 2026-10-13.**

## ADR-16 (new) · Event-store product — **DECIDED: DynamoDB (AWS), scoped to the game-event store only**

> Refines ADR-10's "game truth = append-only event store" by pinning the concrete
> product, which ADR-10 deliberately left open (it named Postgres for relational
> and Redis for ephemeral but not the event store's technology). **DECIDED by the
> owner 2026-10-14**, surfaced via SPEC-S2-GAME-SERVER-GS4 Q-GS4A.

**Context.** GS-4 implements the ratified `game.EventStore` interface and must
pick a concrete store. The access pattern is narrow and well-defined: per-stream
append-only writes keyed `(stream_id, seq)`, ordered full-stream reads for
replay/resync (NFR-9, §3.5), a persist-before-ack durable commit on the move hot
path (NFR-7/NFR-1), an atomic multi-event terminal append (GS-4 §3.4), and a
strict gap-free seq with conflict rejection (§4A.8). This is a textbook
key-value / log fit, not a relational one. The business is **greenfield on the
cloud** (no prior non-AWS investment to write off), so there is no migration cost
pushing toward portability, and the cheapest, lowest-ops posture is to pick one
cloud's best-fit primitive.

**Decision.** Use **Amazon DynamoDB** as the game-event store: a single table
keyed `stream_id` (partition) / `seq` (sort), on-demand capacity for F-01 (no
idle cost, scales linearly with traffic). Its native primitives match the
contract directly — a **conditional put** on the sort key gives the exact
`ErrSeqConflict` seq-rejection semantics for free, and **`TransactWriteItems`**
gives the atomic terminal append (ClockFlagged+GameEnded) natively, both of which
had to be layered onto SQL under the earlier Postgres proposal. Rows carry a
per-item `encoding_version` (GS-4 §3.1) so the stream stays replayable across any
future wire-codec evolution (NFR-9). The choice stays behind the `game.EventStore`
interface (GS-1), so a different store can replace it at G-E scale with no change
to the session/pipeline.

**Scope / boundary — deliberately narrow.** This ADR pins the **event store
only**. It does NOT ratify AWS as the platform for anything else: ADR-10's other
roles stay abstract and portable — accounts/ratings remain "PostgreSQL," cache
remains "Redis," object storage remains "S3-compatible," analytics remains a
"columnar warehouse." Which cloud *those* run on (RDS vs self-run Postgres,
ElastiCache vs Redis, etc.) is a **separate, still-open decision** to be made
deliberately as each chunk (accounts, matchmaking, media, warehouse) comes up —
not pre-committed by this storage choice. The tradeoff accepted here is a bounded,
interface-abstracted AWS/DynamoDB coupling for the truth store; the tradeoff
explicitly NOT accepted is silent AWS-as-platform lock-in by accumulation.

**Alternatives rejected.** *Postgres* (RDS/Aurora) — correct and portable, but a
relational engine is not shaped like an append-only log; it is the right
*relational* store (ADR-10) but would hit a write-throughput + operational cliff
as the *event* store at G-E, requiring self-built sharding. *Dedicated event-log
products* (Kafka/Redpanda, EventStoreDB) — Kafka is a feed, not a transactional
queryable truth store (no per-game atomic multi-append or easy point resync);
EventStoreDB fits the domain but is a 24/7 self-run cluster — real standing infra
+ ops cost now for scale F-01 does not need. *Cassandra/ScyllaDB* — good fit,
portable, but heavier ops than serverless DynamoDB for a greenfield team.

**Unblocks.** SPEC-S2-GAME-SERVER-GS4 (Q-GS4A resolved) and the GS-4 build.

### ADR summary



| ADR | Open | Decision | Status | Owner |
|---|---|---|---|---|
| ADR-1 | OPEN-4 | Glicko-2, per (format×variant) pool, event-sourced | **DECIDED** | S3 |
| ADR-2 | OPEN-5 | Build anti-cheat in-house; permanent standing function | **DECIDED** | S6 |
| ADR-3 | OPEN-6 | Generalized N-seat container now; N-player engine P3 | **DECIDED** | S2/S3 → S1 |
| ADR-4 | OPEN-11 | Pin variant rules to Lichess/FIDE; Spell Chess v1 defined | **DECIDED** | S1 |
| ADR-5 | OPEN-9 | Adopt 1–52 master mapping; use F-xx IDs | **DECIDED** | All |
| ADR-6 | OPEN-13 | Global launch, GDPR baseline, COPPA-as-needed | **DECIDED** (legal confirms specifics) | S6 + legal |
| ADR-7 | OPEN-12 | i18n-ready P0; locale list deferred | **DECIDED** | S4/S5/Platform |
| ADR-8 | OPEN-10 | Freemium membership; entitlements hook P0 | **DECIDED** | S8 |
| ADR-9 | deferred | Native (Swift+Kotlin) over shared Rust core | **DECIDED** | S5/S1 |
| ADR-10 | new | Postgres + event store + Redis + columnar; WS binary protocol | **DECIDED** | S2/S3 |
| ADR-11 | delegated | Rust for engine + shared client core | **DECIDED** | S1/S5 |
| ADR-12 | delegated | Go for services, Rust for engine/core | **DECIDED** | S2/S3/S6/S7 |
| ADR-14 | new | One schema-defined binary wire codec in the shared Rust core | **DECIDED** | S2/S4/S5 |
| ADR-15 | new | Go↔Rust via one uniform in-process cgo/staticlib C-ABI (a `<crate>-cgo` sibling per consumed crate) | **DECIDED** | S2/S1 + Architecture |
| ADR-16 | new | Event-store product = DynamoDB (AWS), scoped to the game-event store only; AWS-as-platform NOT ratified | **DECIDED** | S2 + Architecture |

---

# PART VII — OPEN ITEMS & WHAT NEEDS YOU

## VII.1 Remaining open items (not yet architecture-blocking)

| ID | Item | Needs |
|---|---|---|
| OPEN-1 | Lessons content scope (F-14) | Define lesson taxonomy & initial catalog |
| OPEN-2 | Community "Others" | Enumerate remaining community features |
| OPEN-3 | Named bot personalities (F-02.2) | Yes/no + how many + licensing/branding |
| OPEN-7 | "Tools" definition (F-42) | List exact tools (board editor, PGN viewer, FEN tools, etc.) |
| OPEN-8 | Shop/Merch commerce scope (F-44) | Physical vs digital; fulfillment; when it launches |

*(OPEN-4, 5, 6, 9, 10, 11, 12, 13 are resolved in Part VI. The previously-deferred mobile stack and datastore/transport decisions are now resolved as ADR-9 and ADR-10.)*

## VII.2 Sign-off status — all four resolved (2026-10-11)

The four decisions that required your authority are **answered and folded into the ADRs**:

1. **ADR-2 Anti-cheat** — ✅ **Yes.** Fair-play is a permanent standing function (S6 staffs DS + review + appeals).
2. **ADR-4 Variant rules** — ✅ **Spell Chess v1 ruleset designed** (mana + 8-spell book, deterministic/auditable — see ADR-4); **Fog of War** and **Duck Chess** confirmed **standard** (Lichess rules, win by king-capture).
3. **ADR-6 Compliance** — ✅ **Global launch, COPPA as needed**; ChessKid ships with COPPA controls. One non-blocking follow-up remains: **legal to confirm per-market specifics** and sign the launch-region checklist before G-F (encode now, confirm before launch).
4. **ADR-8 Monetization** — ✅ **Freemium membership**; entitlements hook built at P0.

Nothing architecture-blocking remains open. The only outstanding item is the **legal review** under ADR-6, which is a pre-launch (G-F) gate, not a build blocker — the team proceeds now.

---

# PART VIII — DATA MODEL & API CONTRACT (v0 direction)

> Status: **recommended v0** for the architecture group to ratify in P0, not a frozen spec. Depth is "buildable starting point." The exhaustive endpoint-by-endpoint spec is a follow-on artifact per stream. Frozen version becomes **G-D**.

## VIII.1 Design principles
1. **The game is an event log.** A game's truth is an ordered, append-only sequence of events; current board state is a deterministic fold of those events (ADR-10). This gives replay, audit, and anti-cheat signals for free.
2. **Server is authoritative.** Clients send *intents* (a proposed move); the server validates against the rules engine (X-1) and emits the authoritative event. Clients never compute game truth.
3. **One protocol, three clients.** Web/iOS/Android speak the same wire protocol (NFR-10); the shared Rust core (ADR-9) encodes/decodes it identically everywhere.
4. **Hot path is binary & minimal.** Live move frames are compact binary over WebSocket; everything non-realtime is REST/JSON.

## VIII.2 Core entities (relational — Postgres)
```
User            id, handle, email(hashed+enc), created_at, country, flags, kid_account(bool), locale
Credential      user_id, type(oauth/password/webauthn), provider, secret_ref
Profile         user_id, display_name, avatar_ref, bio, title(GM/IM/…|null), privacy
RatingPool      id, format(bullet/blitz/rapid/daily), variant(standard/chess960/…)   -- (format×variant)
Rating          user_id, pool_id, rating, rd, volatility, games_played, provisional(bool), updated_at
Game            id, pool_id, rated(bool), status, start_fen, result_vector, started_at, ended_at,
                time_control, source(online/bot/friend/tournament), termination(mate/resign/flag/…)
GameSeat        game_id, seat_idx, user_id|bot_id, color, clock_initial, clock_remaining   -- N-seat (ADR-3)
Move            game_id, ply, seat_idx, uci, san, clock_after_ms, server_ts   -- projection of the event log
Puzzle          id, fen, solution_uci[], themes[], rating, source_game_id
PuzzleAttempt   user_id, puzzle_id, solved(bool), ms, rating_delta, ts
Club / Membership / Friendship / Challenge / Tournament / TournamentEntry
ContentItem     id, kind(lesson/course/article/news/blog), slug, locale, body_ref, status
Entitlement     user_id, capability, plan, granted_at, expires_at   -- ADR-8 hook
```
Rating changes and game results are **immutable once written** (NFR-7); corrections are new compensating events, never in-place edits.

## VIII.3 Game-event model (event store — append-only)
Every game is a stream `game/{id}` of ordered events. Minimum event set:
```
GameCreated      {pool, rated, seats, start_fen, time_control}
PlayerJoined     {seat_idx, user_id}
MovePlayed       {ply, seat_idx, uci, clock_after_ms, server_ts}
DrawOffered / DrawAccepted / Resigned / Aborted / TakebackReq / TakebackAccepted
ClockFlagged     {seat_idx}
GameEnded        {result_vector, termination}
RatingApplied    {pool, per-seat {before, after, rd, vol}}
```
Board state, PGN, and the `Move` table are **projections** of this stream. Anti-cheat (ADR-2) subscribes to the same stream.

## VIII.4 Wire protocol — live play (WebSocket, binary)
Message types (client→server `C`, server→client `S`), carried as a compact binary frame (type byte + payload):
```
C  AUTHENTICATE   {token}
C  SEEK           {pool, time_control, rated}         -- enter matchmaking
C  MOVE           {game_id, ply_expected, uci}        -- intent; ply_expected guards races
C  OFFER_DRAW / RESIGN / ABORT / TAKEBACK / REMATCH   {game_id}
C  PING           {t}                                 -- clock sync
S  MATCHED        {game_id, seat_idx, opponent, start_state}
S  MOVE_ACCEPTED  {game_id, ply, clock_after_ms}      -- authoritative
S  MOVE_REJECTED  {game_id, ply_expected, reason}     -- illegal/stale
S  STATE          {game_id, fen, clocks, turn}        -- full resync on reconnect
S  GAME_END       {game_id, result_vector, termination, rating_delta}
S  PONG           {t, server_t}                        -- clock sync (NFR-1 drift budget)
S  ERROR          {code, msg}
```
Reconnect: client re-auths, sends last known `ply`, server replies `STATE` to resync (NFR-2 no-game-loss).

## VIII.5 REST/JSON surface (non-realtime) — representative, not exhaustive
```
POST /v1/auth/login | /register | /oauth/{provider}       -- X-3
GET  /v1/users/{handle} | /v1/users/{handle}/stats         -- F-05/F-46
GET  /v1/games?filters…   POST /v1/games/{id}/analysis     -- F-08/F-17
GET  /v1/games/{id}.pgn                                     -- F-08.4 download
GET  /v1/puzzles/next?themes=&rating=   POST /v1/puzzles/{id}/attempt   -- Section B
GET  /v1/leaderboards/{pool}  GET /v1/ratings/{pool}        -- F-25/26/27
GET/POST /v1/tournaments…  /v1/clubs…  /v1/friends…         -- F-06/F-22/F-23
GET  /v1/content/{kind}/{slug}?locale=                      -- X-8
GET  /v1/me/entitlements                                    -- ADR-8
```
Conventions: versioned (`/v1`), cursor pagination, `ETag`/conditional GET on read-heavy lists, idempotency keys on writes, RFC-7807 problem+json errors, per-user + per-IP rate limits (Part XI).

## VIII.6 What this unblocks and what's still owed
- **Unblocks:** S4/S5 build clients against VIII.4–VIII.5; S2 implements the server side; S3 owns the entity model; G-D freezes a hardened version of this.
- **Still owed (follow-on specs, per stream):** full endpoint catalog with schemas, the binary frame byte-layout, variant-specific event extensions (drops for Crazyhouse, duck placement for Duck Chess, N-seat turn order), and the matchmaking algorithm detail.

---

# PART IX — NON-FUNCTIONAL BUDGETS (measured)

Concrete numbers so load tests (G-E) and capacity planning have a target. **These are proposed launch budgets — ratify/adjust with the architecture group; they are deliberately explicit so they can be argued with.**

## IX.1 Latency budget (NFR-1)
| Metric | p50 | p99 | Hard ceiling |
|---|---|---|---|
| Move intent → authoritative ack (same region) | 50 ms | 150 ms | 300 ms |
| Move intent → ack (cross-region) | 120 ms | 300 ms | 500 ms |
| Matchmaking seek → matched (populated pool) | 2 s | 8 s | 30 s |
| Page/API read (p99) | — | 200 ms | 500 ms |
| Clock drift accumulated over a game | — | 50 ms | 100 ms |

## IX.2 Capacity budget (NFR-3) — launch target
| Dimension | Launch target | Design headroom |
|---|---|---|
| Concurrent connections | 1,000,000 | 3× |
| Concurrent live games | 200,000 | 3× |
| Peak moves/sec | 50,000 | 4× |
| New games/day | 20,000,000 | — |
| Puzzle attempts/sec | 10,000 | 3× |

Scale model: stateless API tier (autoscaled); real-time tier **sharded by game_id** (a game lives on one node, nodes are cattle); Redis cluster for pools/clocks; event store partitioned by game_id; PG read-replicas per region.

## IX.3 Storage & growth
| Data | Per-unit | At launch scale/yr |
|---|---|---|
| Game events | ~2–5 KB/game | ~20M games/day → ~30–75 TB/yr raw (compress + tier cold to object storage) |
| Analytics (columnar) | derived | sized to warehouse budget; TTL raw play events after fold |
| Media/PGN/courses | variable | object storage + CDN, cheap |

Retention policy (set now): hot event store keeps N months; older games fold to compact PGN + archived event blobs in object storage, still replayable.

## IX.4 Cost envelope (to be filled by architecture group)
Placeholder for per-region monthly cost ceiling and $/MAU target. Flagged explicitly so "money is no concern to *build*" doesn't silently become unbounded *run* cost — track run-cost per active user from the first load test.

---

# PART X — TEST & QUALITY STRATEGY

For a chess platform, **the test suite is the spec** (NFR-5). Correctness is not negotiable and is a hard gate (G-A).

## X.1 The engine conformance corpus (owned by S1, built P0, is G-A)
1. **Perft suite** — node-count verification from known positions (startpos + Kiwipete + a dozen tricky FENs) to depth 6–7. A single off-by-one in move generation fails perft. This is the backbone.
2. **Legal-move edge-case library** — explicit cases: en passant only on the immediate reply; en passant that would expose the king (illegal); castling through/into/out of check; castling rights lost by rook capture; promotion to all 4 pieces incl. underpromotion; pins (absolute vs relative); discovered check; double check (only king moves).
3. **Game-end detection** — checkmate, stalemate, threefold repetition (by *position*, incl. castling/en-passant rights, not move list), fivefold auto, 50-move and 75-move auto, insufficient material (K vs K, K+B, K+N, K+B vs K+B same color).
4. **Per-variant conformance suite** — each variant (ADR-4) gets its own corpus; a variant ships only when its suite is 100% green.
5. **Golden-game replay** — a library of complete real games replayed move-by-move, asserting state at every ply; also the regression net (any bug becomes a new golden case).

## X.2 Test pyramid (all streams)
| Layer | What | Where it runs |
|---|---|---|
| Unit | pure logic (rules, rating math, parsers) | every commit, fast |
| Property-based | rating monotonicity, move-gen invariants, serialization round-trips | every commit |
| Contract | API/WS schema conformance; the **shared protocol suite runs on web + iOS + Android** (NFR-10) | PR + nightly |
| Integration | service-to-service (game server ↔ rating ↔ event store) | PR + nightly |
| End-to-end | a real game played through real clients | nightly + pre-release |
| Load/chaos | capacity (IX.2) + node-kill/reconnect/network-partition | pre-gate G-E |
| Security | SAST, dependency scan, DAST, pen-test | continuous + pre-launch |

## X.3 Rating-system tests (G-C)
Determinism (same game sequence → identical ratings bit-for-bit), auditability (replay the rating event log → reconstruct any user's rating history), and known-vector tests against reference Glicko-2 implementations.

## X.4 Release gates (CI-enforced)
No merge to main if: engine corpus < 100%, contract suite failing on any platform, coverage below threshold on changed code, i18n hard-coded-string check fails (NFR-11), or a new dependency flags a CVE. No deploy to a *rated* environment unless G-A, G-B, G-C are green.

---

# PART XI — SECURITY & ABUSE THREAT MODEL

OWASP ASVS L2 baseline (NFR-6) **plus** the chess-specific threats generic checklists miss. Every threat below has a mapped control; anti-cheat (ADR-2) is one row, not the whole model.

| ID | Threat | Control |
|---|---|---|
| T-1 | **Move injection / illegal move** | Server-authoritative validation (X-1); client moves are intents only; reject + log |
| T-2 | **Clock manipulation** (lag-cheating, fake latency) | Server-side clock is truth; measured RTT via PING/PONG; lag-comp bounded; anomaly flags |
| T-3 | **Engine assistance (cheating)** | ADR-2 fair-play system (engine-correlation + behavioral + review) |
| T-4 | **Rating manipulation** — sandbagging, boosting, win-trading, smurfing | Pattern detection on rating graphs & opponent graphs; new-account provisional handling; collusion-ring clustering |
| T-5 | **Account takeover** | WebAuthn/2FA option, breach-password check, session binding, anomalous-login alerts |
| T-6 | **Games-DB scraping / data exfiltration** | Rate limits, auth on bulk endpoints, bot detection, ToS; public data via throttled API only |
| T-7 | **Matchmaking abuse / DoS of pools** | Per-user seek limits, pool integrity checks, queue fairness |
| T-8 | **Abuse in social surfaces** (chat, forums, clubs, messages) | X-11 moderation: filters, reporting, rate limits, shadow/mute, human review (F-32/47/48) |
| T-9 | **Child-safety (ChessKid)** | COPPA controls, restricted comms, no open chat, verified-adult coach gating (F-41, ADR-6) |
| T-10 | **API abuse / credential stuffing** | Per-IP + per-user rate limits, WAF, bot challenge on auth, device fingerprint |
| T-11 | **Payment fraud / entitlement forgery** | Server-verified entitlements (ADR-8), no client-trusted plan state, PCI-DSS via provider |
| T-12 | **Insider / data access** | Least-privilege, audit logs on PII access, mTLS between services, secrets in a vault not code |
| T-13 | **Anti-cheat evasion arms race** | Signal collection breadth (ADR-2), model retraining loop, keep detection logic private |
| T-14 | **DDoS on real-time tier** | Edge scrubbing, connection rate limits, graceful shed of non-core (NFR-2 degradation) |

Standing practices: threat-model review each new surface; secrets in a vault; dependency & container scanning in CI (X.4); annual external pen-test before launch and yearly after.

---

# PART XII — DEFINITION OF DONE & LAUNCH BAR

"Launch when quality warrants" needs a concrete bar. Two definitions:

## XII.1 Definition of Done (per feature/module)
A module is *done* only when: spec'd against its F-id; implemented on all target platforms for its tier; unit + contract + integration tests green; accessibility checked (NFR-8); i18n-externalized (NFR-11); observability wired (metrics + traces); docs/runbook updated; and its acceptance criteria demonstrably met. "Code complete" ≠ done.

## XII.2 Public-launch bar (the "quality warrants" checklist)
Launch is authorized only when **all** hold:
- [ ] Gates **G-A, G-B, G-C, G-D, G-E, G-F** all green.
- [ ] Core loop (Tier 0 + Tier 1 modules) is *done* (XII.1) on web + iOS + Android.
- [ ] Load test sustains IX.2 launch capacity with latency within IX.1 budgets.
- [ ] Chaos test: single-node loss and network partition cause **zero in-progress-game loss** (NFR-2/7).
- [ ] DR drill passes: restore from backup within RTO ≤ 15 min, RPO ≤ 5 s (Part XIII).
- [ ] Fair-play: review queue staffed, SLA < 72 h provable, false-positive rate measured < 0.1%.
- [ ] Security: external pen-test closed to zero high/critical; all Part XI threats have an active control.
- [ ] Compliance (ADR-6): legal sign-off on the global launch-region checklist; COPPA controls live for ChessKid (F-41) and any under-13 users.
- [ ] Observability: SLO dashboards + burn-rate alerts live; on-call rotation staffed.
- [ ] Rollback: a tested one-command rollback for every deployable service.

Below this bar it stays in closed/open beta. There is no deadline (your constraint) — the bar is the deadline.

---

# PART XIII — OPERATIONS & RESILIENCE

| Area | Requirement |
|---|---|
| **Backup / DR** | Continuous event-store replication; PG PITR; cross-region replicas. RPO ≤ 5 s, RTO ≤ 15 min (NFR-7). Quarterly restore drills. |
| **In-progress game survival** | A game is sharded to one node but its event stream is durably replicated; node death → game rehydrates on another node from the stream; clients resync via `STATE` (VIII.4). Zero game loss is a launch gate (XII.2). |
| **On-call / incident** | Rotation per stream for its services; sev levels; runbooks; blameless postmortems; error budgets tied to SLOs (NFR-2). |
| **Rollout** | Feature flags for every risky change; progressive/canary deploys; one-command rollback per service (XII.2). |
| **Rate limiting** | Per-user + per-IP policies at the edge (Part XI); documented limits per endpoint class. |
| **Versioning / migration** | `/v1` API; event-store schema is additive-only (new event types, never mutate old); migration playbook for projections; mobile clients must tolerate N-1 server versions. |
| **Observability** | OpenTelemetry traces on the play path; metrics → SLO dashboards; structured logs; every game replayable (NFR-9). |

---

# PART XIV — ESTIMATION & STAFFING

Rough **t-shirt sizing** to surface where the team is thin — not a commitment, a planning aid. Durations assume a large, well-staffed team running streams in parallel; your no-deadline constraint means these are *sequencing* estimates, not promises.

| Stream | Relative weight | Hardest part | Thin-team risk |
|---|---|---|---|
| S1 Engine | XXL | Variant correctness at 100% (NFR-5); NNUE integration | Needs rare deep-chess-engineering talent — **staff first & heaviest** |
| S2 Real-time | XXL | Scale (IX.2) + zero-loss resilience | Distributed-systems depth is the bottleneck |
| S3 Platform | L | Rating determinism (G-C); event store | — |
| S4 Web | L | Board UX, analysis UI | — |
| S5 Mobile | L | Two native apps + Rust FFI (ADR-9) | FFI tooling ramp |
| S6 Trust & Safety | M→L (grows) | Anti-cheat ML + review ops (standing cost) | Specialized DS + ops hiring (ADR-2) |
| S7 Learn/Content | M | Volume of content, CMS | Content authoring is ongoing, not one-time |
| S8 Community/Watch/Commerce | M | Media pipeline (X-10); moderation at scale | — |

Phase sizing (parallel, indicative): **P0** foundations ~1 quarter; **P1** core loop (web) ~1–2 quarters; **P2** mobile parity + depth ~2 quarters; **P3–P4** breadth ~ongoing; **P5** hardening + launch bar ~1 quarter of focused closure. The critical path is **S1→G-A→S2 live play**; everything else parallelizes around it.

> **Gap this surfaces:** the two XXL streams (engine, real-time) gate everything and need your scarcest talent. If the large team is weighted toward app/feature engineers rather than systems/engine engineers, that's the staffing risk to fix *first*.



- **A PLAY** → F-01…F-08
- **B PUZZLES** → F-09…F-13
- **C LEARN** → F-14…F-15
- **D TRAIN** → F-16…F-22-GR
- **E COMMUNITY** → F-22…F-32
- **F OTHERS** → F-33…F-45
- **G MANAGEMENT** → F-46…F-49
- **H WATCH** → F-50…F-52

*End of master document v2.0 — FOUNDATION baseline. Single source of truth; supersedes all prior split documents. Sections marked "v0 direction" / "proposed" are for architecture-group ratification in P0.*
