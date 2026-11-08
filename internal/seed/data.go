package seed

// The demo's vocabulary. Everything here is fictional: placeholder people
// (Jane Doe, Max Mustermann, …), made-up bands, channels, shops and streets.
// Real-looking but invented, so the timeline reads like a product demo rather
// than lorem ipsum.

// self is the demo persona. Her handle differs per platform, as it would.
var self = struct {
	Name, Reddit, Twitter, Instagram, TikTok, Snapchat, Discord, Telegram, Phone, Email string
}{
	Name: "Jane Doe", Reddit: "u/jane_doe_archives", Twitter: "@janedoe", Instagram: "jane.doe.pics",
	TikTok: "@janedoe.tok", Snapchat: "janedoe_snaps", Discord: "janedoe#0420", Telegram: "Jane Doe",
	Phone: "+49 170 0000000", Email: "jane.doe@example.com",
}

// person is someone the persona interacts with.
type person struct {
	Name   string
	Handle string // platform-neutral short handle, without @
	Phone  string
	City   string
}

var cast = []person{
	{"Max Mustermann", "max_mustermann", "+49 171 0000001", "Berlin"},
	{"Erika Mustermann", "erika.m", "+49 171 0000002", "Hamburg"},
	{"John Q. Public", "jqpublic", "+1 555 0100", "Portland"},
	{"Alice Example", "alice.example", "+1 555 0101", "Seattle"},
	{"Bob Example", "bob_example", "+1 555 0102", "Austin"},
	{"Jane Roe", "jane.roe", "+44 7700 900000", "Bristol"},
	{"Richard Roe", "rroe", "+44 7700 900001", "Leeds"},
	{"Anna Nováková", "anna.nov", "+420 601 000000", "Brno"},
	{"Kofi Mensah", "kofi.m", "+233 20 000 0000", "Accra"},
	{"Sakura Tanaka", "sakura.t", "+81 90 0000 0000", "Kyoto"},
	{"Luca Rossi", "luca.rossi", "+39 320 000 0000", "Bologna"},
	{"Fatima Zahra", "fatima.z", "+212 600 000000", "Rabat"},
}

// friends is the close circle: the people in most threads.
var friends = cast[:5]

var groupChats = []string{"Family 🏠", "Hiking crew", "Flat 4B", "Board game night", "Marathon 2026", "Uni reunion"}

// chatLines are casual messenger lines; {name} is replaced by a friend's name.
var chatLines = []string{
	"are we still on for tonight?",
	"running 10 min late, sorry!!",
	"did you see the photos {name} posted?",
	"ok sounds good 👍",
	"haha no way",
	"can you send me the address again?",
	"I'll bring the cake, you bring the plates",
	"what time does the train leave?",
	"just landed ✈️",
	"omw",
	"the weather looks terrible for saturday, plan B?",
	"remind me to return your book",
	"have you tried the new place on Musterstraße?",
	"yes!! finally",
	"I can't make it this week, next?",
	"call me when you're free",
	"lol that's exactly what I said",
	"bought the tickets, forwarding the confirmation",
	"who's driving?",
	"happy birthday!!! 🎉🎂",
	"so proud of you",
	"we should do this more often",
	"send me the recipe pls",
	"got it, thanks",
	"is this the right link?",
	"the kids say hi 👋",
	"parking is a nightmare there, take the tram",
	"good morning ☀️",
	"see you at 7",
	"can't talk now, in a meeting",
	"that movie was so bad it was good",
	"I left my charger at yours, can you check?",
	"guess who I ran into today",
	"Photo",
	"voice message (0:42)",
	"no worries, feel better soon 🤒",
	"the vet says Kernel is fine, just dramatic",
	"nice shot!",
	"3-1, what a match",
	"can we move it to 8?",
}

var subreddits = []string{
	"selfhosted", "golang", "sveltejs", "DataHoarder", "programming", "homelab", "privacy", "linux",
	"AskHistorians", "hiking", "sourdough", "MechanicalKeyboards", "berlin", "personalfinance",
	"photography", "gardening", "boardgames", "running", "coffee", "Baking",
}

var redditPostTitles = []string{
	"Finally moved my whole archive pipeline to a single box",
	"What's your backup strategy for takeout exports?",
	"Show r/%s: a timeline of everything I ever posted",
	"Keyset pagination beats OFFSET — a war story",
	"Is anyone else hoarding their own data exports?",
	"Hexagonal architecture in Go: worth it for small apps?",
	"My homelab now ingests my social media exports nightly",
	"PSA: request your data exports regularly",
	"First 100% hydration loaf — crumb shot inside",
	"Which trail around Berchtesgaden for a first-timer?",
	"Budget mechanical keyboard that won't wake the neighbours?",
	"How do you organise 200k RAW files?",
	"Ran my first half marathon today (2:04)",
	"What's the best board game for exactly five players?",
	"Espresso tastes sour no matter what I do — help",
	"Tomatoes on a north-facing balcony: doomed?",
	"Best way to learn Svelte coming from React?",
	"Postgres vs SQLite for a single-user app?",
	"Rate my cable management (be gentle)",
	"Anyone here still self-hosting email in %d?",
}

var redditBodies = []string{
	"After years of procrastinating I finally sat down and wrote an importer for all my exports. The mojibake in the Facebook JSON nearly broke me.",
	"Long post incoming. I benchmarked keyset vs offset pagination on 2M rows and the difference is not subtle…",
	"Honestly the hardest part was normalizing six different timestamp formats into one canonical event.",
	"It runs in Docker, speaks Postgres, and the whole thing is one binary. AMA.",
	"The trick is to never drop data: anything you can't parse goes into an .unknown bucket for later.",
	"I used to think this was overkill. Then Twitter locked my account for a week and I couldn't see my own tweets.",
	"Details: 80% bread flour, 20% whole wheat, 2% salt, 12h cold retard. Baked in a Dutch oven at 250°C.",
	"Going in late September, staying in Schönau. Fit but not a climber. Photos welcome.",
	"Budget is around 80€. Currently on a laptop keyboard and my wrists are filing a complaint.",
	"Every recommendation I find is either 'use Lightroom' or 'write your own'. Is there a middle ground?",
	"Training plan was 12 weeks, mostly easy runs. The last 3k were entirely willpower and a gel.",
	"We've played Catan to death. Looking for something with more interaction and less trading.",
	"18g in, 36g out, 28 seconds, fresh beans from a local roaster. Still sour. Grinder is a hand grinder.",
	"Gets maybe 3 hours of direct light. I have hope and a grow light.",
	"The mental model shift from 'components re-render' to 'assignments trigger updates' took me a week.",
}

var redditComments = []string{
	"This is the way. I've been doing the same with a cron job and rsync.",
	"Did you consider River for the job queue? Curious why you rolled your own.",
	"Underrated point about SSRF defense — most people skip the egress allowlist.",
	"+1 for lib/pq, though sqlc would save you some boilerplate here.",
	"Came for the memes, stayed for the surprisingly solid schema design.",
	"How do you handle deleted posts that still show up in the export?",
	"FOR UPDATE SKIP LOCKED is criminally underused. Nice writeup.",
	"Grind finer. Sour almost always means under-extraction.",
	"Beautiful crumb! What oven do you use?",
	"Try the Jenner trail — long but not technical, and the views are worth it.",
	"Cherry MX Silent Reds or go Topre and never look back.",
	"Congrats on the half! The first one is the hardest.",
	"Five players: Ra, Modern Art, or Cosmic Encounter if your group likes chaos.",
	"Cherry tomatoes will do fine with 3 hours. Skip the beefsteaks.",
	"Saving this for later, thanks for the detailed writeup.",
	"Came here to say exactly this.",
	"Source? Not doubting you, genuinely curious.",
	"This should be higher up.",
	"Have you tried turning it off and on again? (sorry)",
	"Great post. One nit: your diagram has the arrows backwards in the adapter layer.",
}

var tweets = []string{
	"Shipped a thing: my entire social media history, one searchable timeline. Self-hosted, obviously.",
	"Hot take: your data exports are the best API your platforms will ever give you.",
	"Day 12 of the rewrite. The hexagon is complete. The compiler is pleased.",
	"TIL Facebook exports double-encode UTF-8. Two hours of my life I'm not getting back.",
	"Keyset pagination or bust. OFFSET is a scam at scale.",
	"The best time to request your data export was ten years ago. The second best time is now.",
	"go:embed is criminally convenient for shipping SPAs in a single binary.",
	"My timeline app now has more of my tweets than Twitter shows me. Let that sink in.",
	"Weekend project status: it has a job queue now. It was supposed to be simple.",
	"Naming things update: Event, StoredEvent, RawRecord. I regret nothing.",
	"Morning run, 8k, fog on the river. Berlin is pretty when it wants to be.",
	"Sourdough starter turned 3 today. It has outlived two houseplants and a router.",
	"Unpopular opinion: the best keyboard is the one you already own.",
	"Currently reading: a 600-page book about salt. Yes, the mineral. No regrets so far.",
	"New camera day. Old lens. The old lens wins, as always.",
	"The train is 40 minutes late, which means it's exactly on time by local standards.",
	"Finished the marathon training block. 640 km this quarter. Legs: sceptical.",
	"If your app needs a 'clear cache' button, the cache is the app.",
	"Trying to explain to my mum what a data export is. Progress: she now asks for hers.",
	"Coffee roasted on Tuesday, brewed on Saturday. Peak.",
	"Reminder that 'temporary' in a code comment means 'permanent' in every language.",
	"Ten years ago I signed up here. Today I downloaded all of it. Full circle.",
	"Board game night verdict: Brass is a masterpiece and I lost badly.",
	"Balcony tomato #1 has been harvested. It was the size of a marble. Delicious.",
	"Migrated the blog to static files. Load time went from 3s to 30ms. Should have done it years ago.",
	"The cat has learned to open the server rack. This is now a security incident.",
	"Wrote a PDF by hand today for a test fixture. 1 KB. Opens fine. I feel powerful.",
	"Half marathon done in 2:04. Next stop: the full one, apparently.",
	"You either die a side project or live long enough to become a monorepo.",
	"Just discovered my 2016 tweets. Deleting nothing. Cringe is a form of growth.",
}

var handles = []string{"gopher_dev", "sveltewizard", "datahoarder_jane", "selfhost_sam", "pg_tips_daily", "archival_annie", "indieweb_max", "trailrunner_jo", "sourdough_sally", "keeb_kofi", "berlin_streets", "roastery_notes"}

var igCaptions = []string{
	"Golden hour at the lake. No filter needed. #nofilter #sunset",
	"New homelab shelf day. Cable management pending. #homelab",
	"Sourdough attempt #7. We're calling this one 'rustic'. #baking",
	"Trail run above the fog line this morning. Worth the 5am alarm.",
	"Desk setup v3. The monitor arm changed everything. #battlestation",
	"Weekend hike, 1400m up. Legs: filing a complaint. #hiking",
	"First tomato from the balcony garden. It was delicious and tiny.",
	"City lights from the rooftop. This town never gets old.",
	"Flat white and a slow Sunday ☕ #coffee",
	"Kernel has claimed the keyboard. Work is cancelled. #catsofinstagram",
	"Race day! 21.1 km of questionable decisions. #halfmarathon",
	"Old lens, new camera, same river. #35mm",
	"Market haul: figs, chanterelles and way too much bread.",
	"Board game night. Brass. I lost. Photo for evidence.",
	"Autumn is doing its thing in Volkspark. 🍂",
	"Repotted everything. The balcony is a jungle now. #urbanjungle",
}

var igAccounts = []string{"trailrunner.jo", "homelab.hank", "sourdough.sally", "urbanshots.k", "gardenlog.eva", "peakbagger.tom", "berlin.on.film", "roastery.notes", "keebs.daily", "kernel.the.cat"}

var fbPosts = []string{
	"Throwback to the coast trip — found these while sorting old photos. What a week that was.",
	"PSA for local friends: the farmers market moved to the north lot starting this Saturday.",
	"Ten years ago today I started my first programming job. Time is not real.",
	"We adopted a cat. The router uptime is now at risk. Meet Kernel.",
	"Garage sale next weekend — tools, books, and an honestly concerning number of keyboards.",
	"Made the family recipe tonight for the first time. Grandma would have approved, I think.",
	"Moving day done. 47 boxes, 1 broken mug, 0 friendships lost. Thank you all!",
	"Anyone have a recommendation for a bike shop near Prenzlauer Berg?",
	"Happy new year from the balcony 🎆",
	"Half marathon finisher! Thanks for cheering, you loud wonderful people.",
	"Grandpa's 90th. Four generations in one photo. Feeling very lucky.",
	"Selling: IKEA desk, good condition, pickup only. Comment if interested.",
	"First day at the new job. New badge, new coffee machine, same imposter syndrome.",
	"Snow in Berlin! For about four hours. Enjoy it while it lasts.",
}

var fbComments = []string{
	"Congratulations, this is wonderful news!",
	"Kernel is a perfect name, I'm deceased 😂",
	"Count us in for Saturday!",
	"This brings back so many memories. Thanks for sharing.",
	"Save me one of those keyboards please.",
	"Happy birthday!! Have a great one 🎉",
	"Looks amazing, recipe please?",
	"We miss you guys! Come visit soon.",
	"Interested! Sending you a message.",
	"So proud of you 👏",
	"Where is this? Adding it to the list.",
	"Classic Max 😄",
}

var (
	fbGroups   = []string{"Berlin Runners", "Homelab Germany", "Prenzlauer Berg Neighbours", "Sourdough Bakers Worldwide", "Board Games Berlin", "Balcony Gardeners", "Photography Walks Berlin"}
	fbEvents   = []string{"Berlin Half Marathon", "Board game night at Max's", "Flea market at Mauerpark", "Erika's 30th birthday", "Company summer party", "Long Night of Museums", "Christmas market meetup", "Uni reunion 2019"}
	fbApps     = []string{"Strava", "Spotify", "Duolingo", "Goodreads", "Komoot", "Untappd"}
	fbSearches = []string{"max mustermann", "berlin runners", "marketplace desk", "erika birthday event", "bike shop prenzlauer berg", "sourdough group", "board games berlin", "flat for rent kreuzberg", "kernel cat photo", "half marathon results"}
)

// songs are fictional tracks.
var songs = []struct{ Track, Artist, Album string }{
	{"Neon Harbour", "The Paper Lanterns", "Late Ferries"},
	{"Second Winter", "The Paper Lanterns", "Late Ferries"},
	{"Salt & Static", "The Paper Lanterns", "Late Ferries"},
	{"Copper Sky", "Marlowe Vance", "Low Tide"},
	{"Small Hours", "Marlowe Vance", "Low Tide"},
	{"Driftwood", "Marlowe Vance", "Low Tide"},
	{"Glass Orchard", "Ines Falk", "Glass Orchard"},
	{"Paper Boats", "Ines Falk", "Glass Orchard"},
	{"A Room With Two Windows", "Ines Falk", "Glass Orchard"},
	{"Overpass", "Kilometre Zero", "Ring Road"},
	{"Night Bus", "Kilometre Zero", "Ring Road"},
	{"Turnstile", "Kilometre Zero", "Ring Road"},
	{"Blue Hour", "Wren & Ash", "Sonder"},
	{"Cartography", "Wren & Ash", "Sonder"},
	{"Halflight", "Wren & Ash", "Sonder"},
	{"Motorik", "Autobahn Kinder", "Kraut Salat"},
	{"Spree", "Autobahn Kinder", "Kraut Salat"},
	{"Betonwiese", "Autobahn Kinder", "Kraut Salat"},
	{"Tangerine Radio", "DJ Marmalade", "Breakfast Club Mix"},
	{"Toast (Extended)", "DJ Marmalade", "Breakfast Club Mix"},
	{"Sunrise on Repeat", "DJ Marmalade", "Breakfast Club Mix"},
	{"Prelude in Grey", "Ottilie Marsh", "Études for Rainy Days"},
	{"Étude No. 4", "Ottilie Marsh", "Études for Rainy Days"},
	{"Nocturne for a Kettle", "Ottilie Marsh", "Études for Rainy Days"},
	{"Kilometres", "The Long Run", "Negative Split"},
	{"Cadence", "The Long Run", "Negative Split"},
	{"Wall at 30k", "The Long Run", "Negative Split"},
	{"Threadbare", "Hollow Pines", "Undergrowth"},
	{"Moss", "Hollow Pines", "Undergrowth"},
	{"Firebreak", "Hollow Pines", "Undergrowth"},
	{"Espresso Shot", "Bean Counter", "Third Wave"},
	{"Pour Over", "Bean Counter", "Third Wave"},
	{"Grind Size", "Bean Counter", "Third Wave"},
	{"Ferris Wheel", "Mona Pastel", "Fairground"},
	{"Candyfloss", "Mona Pastel", "Fairground"},
	{"Hall of Mirrors", "Mona Pastel", "Fairground"},
	{"Ctrl+Z", "The Undo Stack", "Refactor"},
	{"Merge Conflict", "The Undo Stack", "Refactor"},
	{"Garbage Collector", "The Undo Stack", "Refactor"},
	{"Lantern Festival", "Sakura Drift", "Kyoto Tapes"},
	{"Bamboo Grove", "Sakura Drift", "Kyoto Tapes"},
	{"Rain on Tatami", "Sakura Drift", "Kyoto Tapes"},
	{"Accra Nights", "Kofi & The Highlife Sextet", "Gold Coast"},
	{"Market Day", "Kofi & The Highlife Sextet", "Gold Coast"},
	{"Palm Wine", "Kofi & The Highlife Sextet", "Gold Coast"},
	{"Ostsee", "Nordlicht", "Küste"},
	{"Dünen", "Nordlicht", "Küste"},
	{"Leuchtturm", "Nordlicht", "Küste"},
	{"Ascent", "Peak Radio", "Above the Treeline"},
	{"Scree", "Peak Radio", "Above the Treeline"},
	{"Summit Cross", "Peak Radio", "Above the Treeline"},
	{"Bolognese", "Luca & Sons", "Trattoria"},
	{"Piazza", "Luca & Sons", "Trattoria"},
	{"Vespa", "Luca & Sons", "Trattoria"},
	{"Late Ferries", "The Paper Lanterns", "Late Ferries"},
	{"Encore", "Marlowe Vance", "Live at the Kesselhaus"},
	{"Small Hours (Live)", "Marlowe Vance", "Live at the Kesselhaus"},
	{"Intro", "Ines Falk", "Glass Orchard"},
	{"Dead End", "Kilometre Zero", "Ring Road"},
	{"Exit 12", "Kilometre Zero", "Ring Road"},
}

var (
	spotifyPlaylists = []string{"Running 180bpm", "Sunday morning", "Focus (no lyrics)", "Road trip 2024", "Kitchen disco", "Liked from radio"}
	spotifySearches  = []string{"paper lanterns", "marlowe vance small hours", "running playlist 180", "ines falk", "kilometre zero", "lofi focus", "highlife", "ottilie marsh etudes", "autobahn kinder", "kraut", "nordlicht küste", "wren and ash", "peak radio", "bean counter espresso", "undo stack", "sakura drift"}
)

// videos are fictional YouTube uploads.
var videos = []struct{ Title, Channel string }{
	{"How to true a bicycle wheel (no stand needed)", "Example Bike Shop"},
	{"Sourdough for people who work full time", "Slow Loaf"},
	{"I ran every street in my city — here's what happened", "Kilometre Club"},
	{"Homelab tour 2026: what stayed, what went", "Rack & Ruin"},
	{"Go generics explained with one example", "Gopher Hour"},
	{"Svelte 5 runes in 12 minutes", "Frontend Fika"},
	{"Postgres indexing: the 20% you need", "Query Plan"},
	{"Why your espresso is sour", "Bean Counter TV"},
	{"Berchtesgaden Alps: 3-day hut trek", "Peak Radio Outdoors"},
	{"The history of the Berlin S-Bahn", "Rails & Tales"},
	{"Restoring a 1970s film camera", "Analog Workshop"},
	{"Balcony tomatoes: full season timelapse", "Gardenlog"},
	{"Brass: Birmingham — full playthrough", "Table Talk"},
	{"Mechanical keyboard sounds ranked (ASMR)", "Keebs Daily"},
	{"Kyoto in autumn — silent walk", "Sakura Drift Films"},
	{"Highlife guitar lesson #3", "Accra Strings"},
	{"How the ferry timetable is made", "Nordlicht Docs"},
	{"Zero-downtime Postgres migrations", "Query Plan"},
	{"Marathon pacing: negative split or bust", "Kilometre Club"},
	{"I self-hosted everything for a year", "Rack & Ruin"},
	{"One-pot pasta that actually works", "Luca Cooks"},
	{"Reading 'Salt' so you don't have to", "Bookish Bob"},
	{"Lo-fi beats to archive data to", "Undo Stack Radio"},
	{"Why cats love server racks", "Kernel & Friends"},
	{"Every Svelte store pattern, benchmarked", "Frontend Fika"},
	{"The last independent lens repair shop in Berlin", "Berlin on Film"},
	{"Trail running shoes: 2026 roundup", "Kilometre Club"},
	{"A week of bread: seven loaves, seven flours", "Slow Loaf"},
	{"Writing a PDF by hand", "Gopher Hour"},
	{"Hexagonal architecture, honestly", "Gopher Hour"},
	{"Cheap NAS build under 300€", "Rack & Ruin"},
	{"Fog running in the Tiergarten", "Berlin Runners"},
	{"How to photograph the blue hour", "Analog Workshop"},
	{"The Undo Stack — Merge Conflict (Official Video)", "The Undo Stack"},
	{"Marlowe Vance — Small Hours (Live at the Kesselhaus)", "Marlowe Vance"},
	{"The Paper Lanterns — Neon Harbour (Lyric Video)", "The Paper Lanterns"},
	{"Ines Falk — Glass Orchard (Full Album)", "Ines Falk"},
	{"Espresso grind size explained", "Bean Counter TV"},
	{"Is this the best board game of the decade?", "Table Talk"},
	{"Repotting a monstera without crying", "Gardenlog"},
}

var youtubeSearches = []string{"wheel truing", "sourdough overnight", "svelte 5 tutorial", "postgres index", "half marathon plan 12 weeks", "espresso sour fix", "berchtesgaden hike", "homelab 2026", "go generics", "keyboard sound test", "kyoto autumn", "highlife guitar", "brass birmingham rules", "monstera repot", "film camera repair", "berlin s-bahn history"}

// sites are pages the persona visits.
var sites = []struct{ Title, URL string }{
	{"Wheel Truing 101 - Example Bike Wiki", "https://bikes.example.org/wiki/wheel-truing"},
	{"Go Playground", "https://go.dev/play/"},
	{"pkg.go.dev - net/http", "https://pkg.go.dev/net/http"},
	{"PostgreSQL: Documentation: 16: SELECT", "https://www.postgresql.org/docs/16/sql-select.html"},
	{"Svelte docs - Runes", "https://svelte.dev/docs/svelte/what-are-runes"},
	{"Slow Loaf — Overnight sourdough", "https://slowloaf.example.com/overnight"},
	{"Kilometre Club — 12 week half marathon plan", "https://kilometre.example.com/plans/half-12"},
	{"Berliner Verkehrsbetriebe - Fahrplan", "https://www.bvg.de/"},
	{"DB Navigator - Berlin → Hamburg", "https://www.bahn.de/"},
	{"Hacker News", "https://news.ycombinator.com/"},
	{"Query Plan — Indexing the 20%", "https://queryplan.example.com/indexing-20"},
	{"Rack & Ruin — NAS under 300€", "https://rackandruin.example.com/nas-300"},
	{"Wikipedia — Hexagonal architecture", "https://en.wikipedia.org/wiki/Hexagonal_architecture_(software)"},
	{"Wikipedia — Berchtesgaden Alps", "https://en.wikipedia.org/wiki/Berchtesgaden_Alps"},
	{"Komoot — Jenner loop", "https://www.komoot.com/tour/example-jenner"},
	{"Bean Counter — grind chart", "https://beancounter.example.com/grind"},
	{"Gardenlog — balcony tomatoes", "https://gardenlog.example.com/tomatoes"},
	{"Table Talk — Brass rules summary", "https://tabletalk.example.com/brass"},
	{"Keebs Daily — switch guide", "https://keebs.example.com/switches"},
	{"Analog Workshop — lens cleaning", "https://analog.example.com/lens-cleaning"},
	{"GitHub", "https://github.com/"},
	{"GitHub — golang/go issues", "https://github.com/golang/go/issues"},
	{"Stack Overflow — go embed directory", "https://stackoverflow.com/questions/example-embed"},
	{"MDN — Content-Disposition", "https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Disposition"},
	{"Berlin.de — Bürgeramt Termin", "https://service.berlin.de/"},
	{"Weather — Berlin 7 days", "https://weather.example.com/berlin"},
	{"Immobilien — 2 Zimmer Kreuzberg", "https://immo.example.com/search?q=kreuzberg"},
	{"Recipe — one-pot pasta", "https://lucacooks.example.com/one-pot-pasta"},
	{"Nordlicht Docs — Ferry timetable", "https://nordlicht.example.com/ferries"},
	{"Berlin Half Marathon — results", "https://results.example.com/berlin-half"},
	{"Kyoto travel guide", "https://travel.example.com/kyoto"},
	{"Goodreads — Salt: A World History", "https://www.goodreads.com/book/example-salt"},
	{"OpenStreetMap", "https://www.openstreetmap.org/"},
	{"Local roastery — beans", "https://roastery.example.com/beans"},
	{"Docker docs — compose healthcheck", "https://docs.docker.com/compose/"},
	{"Wikipedia — Highlife", "https://en.wikipedia.org/wiki/Highlife"},
	{"Volkspark Friedrichshain — events", "https://volkspark.example.com/"},
	{"Mauerpark flea market", "https://mauerpark.example.com/"},
	{"Example Bike Shop — opening hours", "https://bikes.example.org/"},
	{"Marathon registration", "https://marathon.example.com/register"},
}

var googleSearches = []string{
	"bicycle wheel truing stand", "sourdough hydration calculator", "go embed directory", "postgres for update skip locked",
	"half marathon pace 2:00", "espresso sour fix", "berchtesgaden hut booking", "svelte adapter static",
	"berlin bürgeramt termin", "train berlin hamburg saturday", "cat opens door how to stop", "monstera yellow leaves",
	"brass birmingham 5 players", "weather berlin weekend", "how to write a pdf by hand", "keyset pagination cursor",
	"best hiking boots wide feet", "film developing berlin", "kyoto autumn foliage forecast", "highlife guitar tuning",
	"flat white vs cappuccino", "vet emergency berlin", "docker compose healthcheck", "one pot pasta recipe",
	"marathon registration 2026", "cheap nas build", "ikea desk resale value", "self hosting email 2026",
	"tomato balcony north facing", "wren and ash tour dates",
}

// places are fictional locations around Berlin and a couple of trips.
var places = []struct {
	Name, Address, Type string
	Lat, Lon            float64
}{
	{"Home", "Musterstraße 1, 10115 Berlin", "TYPE_HOME", 52.5200, 13.4050},
	{"Work", "Beispielallee 5, 10117 Berlin", "TYPE_WORK", 52.5170, 13.3889},
	{"Example Bike Shop", "Kastanienallee 8, 10435 Berlin", "", 52.5361, 13.4085},
	{"Slow Loaf Bakery", "Bergmannstraße 21, 10961 Berlin", "", 52.4889, 13.3946},
	{"Bean Counter Roastery", "Weserstraße 60, 12045 Berlin", "", 52.4867, 13.4315},
	{"Volkspark Friedrichshain", "Am Friedrichshain, 10407 Berlin", "", 52.5283, 13.4364},
	{"Tiergarten", "Straße des 17. Juni, 10557 Berlin", "", 52.5145, 13.3501},
	{"Mauerpark", "Gleimstraße 55, 10437 Berlin", "", 52.5437, 13.4024},
	{"Berlin Hauptbahnhof", "Europaplatz 1, 10557 Berlin", "", 52.5251, 13.3694},
	{"Hamburg Hauptbahnhof", "Hachmannplatz 16, 20099 Hamburg", "", 53.5530, 10.0069},
	{"Erika's place", "Eppendorfer Weg 3, 20259 Hamburg", "", 53.5720, 9.9640},
	{"Kesselhaus", "Knaackstraße 97, 10435 Berlin", "", 52.5395, 13.4130},
	{"Jenner summit", "Schönau am Königssee", "", 47.5876, 13.0163},
	{"Königssee boat dock", "Seestraße, 83471 Schönau", "", 47.5911, 12.9881},
	{"Kyoto Station", "Higashishiokoji-cho, Kyoto", "", 34.9858, 135.7588},
	{"Arashiyama Bamboo Grove", "Ukyo Ward, Kyoto", "", 35.0170, 135.6717},
	{"Table Talk Café", "Oranienstraße 190, 10999 Berlin", "", 52.5000, 13.4197},
	{"Max's flat", "Sonnenallee 100, 12045 Berlin", "", 52.4823, 13.4390},
	{"Vet clinic", "Schönhauser Allee 120, 10437 Berlin", "", 52.5468, 13.4128},
	{"Bristol Temple Meads", "Station Approach, Bristol", "", 51.4491, -2.5813},
}

var linkedinPosts = []string{
	"Thrilled to share that I've joined Example Data GmbH as a Senior Backend Engineer! Grateful to everyone who helped along the way.",
	"Three lessons from migrating a monolith to a hexagonal architecture: 1) ports are documentation, 2) adapters are cheap, 3) the domain is the product.",
	"We're hiring! Looking for a Go engineer who likes boring technology and interesting problems. DM me.",
	"Spoke at GoDays Berlin about keyset pagination. Slides in the comments. Thank you for the great questions!",
	"Five years at Example Corp today. Proud of the team and what we built. On to the next chapter.",
	"Hot take for a Monday: your data export is an API. Treat it like one.",
	"Finished the AWS certification. Not because I needed it, but because I wanted to know what I didn't know.",
	"Mentoring two junior engineers this quarter. Teaching is the fastest way to find the gaps in what you know.",
}

var (
	linkedinComments  = []string{"Congratulations! Well deserved.", "Great insights, thanks for sharing.", "Fully agree on point 2.", "Would love to hear more about the migration.", "Inspiring, as always.", "Welcome aboard!", "This resonates."}
	linkedinCompanies = []string{"Example Data GmbH", "Example Corp", "Musterfirma AG", "Acme Analytics", "Northwind Logistics", "Contoso Cloud", "Fabrikam Robotics", "Globex Media"}
	linkedinTitles    = []string{"Senior Backend Engineer", "Engineering Manager", "Data Engineer", "Product Manager", "Staff Engineer", "Solutions Architect", "Recruiter", "CTO", "Frontend Developer", "Site Reliability Engineer"}
	linkedinSearches  = []string{"go developer berlin", "example data gmbh", "max mustermann", "engineering manager remote", "postgres consultant", "godays berlin", "acme analytics", "backend jobs hamburg"}
)

var chatgptTopics = []string{
	"Debugging a Go race condition", "Explain keyset pagination like I'm five", "SvelteKit adapter-static gotchas",
	"Write a regex for ISO 8601 timestamps", "Docker compose healthchecks", "Postgres GIN index for full-text search",
	"Refactor this switch into a lookup table", "Why is my zip reader leaking file handles?",
	"CSV with embedded newlines — parsing strategies", "Idempotent seeds with stable external IDs",
	"HMAC session cookies vs JWT", "Migrating a side project to hexagonal architecture",
	"12-week half marathon plan for a 2:00 finish", "Sourdough: why is my crumb gummy?", "Packing list for a 3-day hut trek",
	"Translate this email to German, formal", "Explain the Berlin rent cap in simple terms", "Espresso troubleshooting checklist",
	"Best board games for exactly five players", "Balcony tomatoes on a north-facing side", "Draft a birthday toast for Erika",
	"Vet says the cat is 'dramatic' — is that a thing?", "Explain Content-Disposition headers", "Write a haiku about a job queue",
	"Kyoto itinerary, 4 days, autumn", "Salt: A World History — summary", "Convert 2:04 half marathon to km pace",
	"What is highlife music?", "Repotting a monstera step by step", "Tips for a first conference talk",
}

var claudeTopics = []string{
	"Review my Go error handling patterns", "Draft a README for a data-archiving app", "Threat-model a self-hosted web app",
	"Normalize six export formats into one schema", "Explain FOR UPDATE SKIP LOCKED", "Svelte reactivity with Sets — pitfalls",
	"Design a keyset cursor encoding", "Vendoring vs proxy in Go modules", "Write table-driven tests for a parser",
	"Plan a migration from lib/pq to pgx", "Write a hand-made minimal PDF", "Explain SigV4 signing without an SDK",
	"Outline a talk on data portability", "Edit my cover letter for a staff role", "Compare S3 and MinIO for a homelab",
	"Design a rate limiter for login attempts", "Summarise the GDPR export rights", "Marathon nutrition plan (vegetarian)",
	"Rewrite this paragraph to be warmer", "Explain CSP nonces vs hashes", "Help me name a cat (it opens doors)",
	"Choose between SQLite and Postgres for one user", "Write a toast for grandpa's 90th", "Debug a flaky Go test",
	"Log-scale histogram: when to switch automatically",
}

var userLines = []string{
	"Here's what I'm seeing — any idea what's wrong?",
	"Can you walk me through this step by step?",
	"That worked. Now how do I make it faster?",
	"What are the trade-offs of that approach?",
	"Show me a minimal example in Go, please.",
	"I tried that but the test still flakes intermittently.",
	"Shorter, please. Bullet points.",
	"Can you make it sound less like a robot wrote it?",
	"OK but what if the input is empty?",
	"Thanks, that's exactly what I needed.",
	"Assume I'm on Linux and have Docker.",
	"Give me the version for a beginner first.",
}

var assistantLines = []string{
	"Great question. The short version: it depends on where the state lives. Let me break it down…",
	"Here's a minimal example that demonstrates the pattern, followed by the trade-offs you asked about.",
	"The flake is almost certainly a shared fixture. Try isolating the store per test and see if it persists.",
	"Two options: the simple one that works today, and the robust one you'll want at scale. I'd start simple.",
	"Your instinct is right — the cursor should encode (occurred_at, id) so pagination is stable under inserts.",
	"That error means the embed directive ran before the dist directory existed. Build the frontend first.",
	"Here is a shorter version:\n\n- Ports are interfaces the application owns.\n- Adapters implement them at the edge.\n- The domain never imports either.",
	"For an empty input, return early with an empty slice rather than nil, so JSON encodes `[]` instead of `null`.",
	"Happy to help! Here's a plan for the 12 weeks, with three runs a week and one long run building from 8 to 18 km.",
	"A gummy crumb usually means under-baked or cut too early. Let it cool for at least an hour.",
	"Sure. In plain terms: the header tells the browser whether to show the file or download it, and what to call it.",
	"Here's a warmer version that keeps your key points but opens with the person rather than the request.",
}

var claudeProjects = []struct{ Name, Desc string }{
	{"Archivist", "Self-hosted timeline of my data exports. Go + SvelteKit + Postgres."},
	{"Marathon 2026", "Training plan, nutrition notes and race-day logistics."},
	{"Kyoto trip", "Itinerary, bookings and a packing list for four days in autumn."},
}

var claudeProjectDocs = []struct{ Title, Body string }{
	{"ARCHITECTURE.md", "Hexagonal: domain ← application ← adapters. The composition root is cmd/server/main.go."},
	{"Providers checklist", "Descriptor, Capabilities, Extractors, Normalize. Golden test per provider. Never drop data."},
	{"Threat model", "Untrusted zips, SSRF via export links, media served from our origin: sandbox CSP, nosniff."},
	{"Week 1–4", "Easy runs Tue/Thu, long run Sunday. 8 → 11 km. Strides on Thursday."},
	{"Race day", "Bib pickup Saturday. Gel at 7k and 14k. Meet Max at the 10k mark."},
	{"Day 1: Arashiyama", "Bamboo grove at 7am before the crowds. Lunch near Togetsukyo bridge."},
	{"Day 3: Fushimi Inari", "Start at the top and walk down. Bring cash for the tea house."},
	{"Packing list", "Rain jacket, JR pass, spare battery, one good lens, comfortable shoes."},
}

var discordServers = []struct {
	Name     string
	Channels []string
}{
	{"Gophers", []string{"general", "help", "showcase", "jobs"}},
	{"Homelab", []string{"general", "networking", "storage", "off-topic"}},
	{"Board Game Night", []string{"general", "scheduling", "brass-strategy"}},
	{"Berlin Runners", []string{"general", "routes", "race-day"}},
	{"Svelte Society", []string{"general", "kit", "help"}},
	{"Kernel Fan Club", []string{"cat-pics"}},
}

var discordLines = []string{
	"anyone know why go vet complains about this?", "pushed a fix, PTAL", "lgtm 🚀", "what's the plan for saturday?",
	"I'll be there around 7", "the new NAS is humming along nicely", "which switch for a 10G backbone?", "brb coffee",
	"that's a classic off-by-one", "has anyone tried the Jenner loop?", "route posted in #routes", "gg everyone",
	"Brass tonight? need one more", "posted a photo of Kernel in #cat-pics", "does SvelteKit support this yet?",
	"read the docs, it's under adapters", "😂😂😂", "^ this", "anyone else's build broken on main?", "fixed in latest",
	"we should do a meetup", "+1", "what time zone are you in?", "CET", "sending the invite now",
}

var tiktokCaptions = []string{
	"POV: your cat discovers the server rack #homelab #cat", "sourdough in 15 seconds #baking", "berlin at 6am is a different city #runtok",
	"desk setup tour, no talking #battlestation", "trail run, no music, just breath #trailrunning", "espresso pull in slow motion ☕",
	"pack with me: 3 day hut trek 🏔️", "board game night chaos #boardgames", "kyoto autumn walk 🍁", "keyboard sounds you didn't ask for",
}

var (
	tiktokCreators = []string{"kernel.the.cat", "slowloaf", "kilometre.club", "rackandruin", "beancounter.tv", "tabletalk", "sakura.drift", "keebs.daily", "gardenlog", "analog.workshop", "berlin.runners", "gopher.hour"}
	tiktokSearches = []string{"cat server rack", "sourdough shaping", "berlin run route", "espresso slow mo", "hut trek packing", "brass birmingham", "kyoto autumn", "keyboard sound", "monstera repot", "trail shoes"}
)

var (
	snapSearches   = []string{"max", "erika", "jqpublic", "berlin lens", "alice example", "bob"}
	snapStories    = []string{"Sunset from the balcony", "Race day!!", "Kernel vs. the box", "Hut trek day 2", "Kyoto 🍁", "Sourdough Sunday", "Board game night", "Snow day"}
	snapPublishers = []string{"Berlin Runners Daily", "Slow Loaf", "Keebs Daily", "Kilometre Club", "Table Talk", "Gardenlog"}
)

var (
	telegramChats = []string{"Max Mustermann", "Erika Mustermann", "Anna Nováková", "Hiking crew", "Homelab DE", "Kyoto planning", "Flat 4B"}
	whatsappChats = []string{"Max Mustermann", "Erika Mustermann", "Family 🏠", "Hiking crew", "Flat 4B", "Board game night", "Marathon 2026", "John Q. Public"}
)

var whatsappSystem = []string{
	"Messages and calls are end-to-end encrypted. No one outside of this chat, not even WhatsApp, can read or listen to them.",
	"%s added %s", "%s left", "%s changed the group description", "%s changed this group's icon", "You created group \"%s\"",
	"%s changed their phone number to a new number. Tap to message or add the new number.",
}

var (
	deviceNames = []string{"Pixel 8", "iPhone 13", "ThinkPad X1 Carbon", "MacBook Air", "iPad", "Firefox on Linux", "Chrome on Windows"}
	cities      = []string{"Berlin, Germany", "Hamburg, Germany", "Munich, Germany", "Bristol, United Kingdom", "Kyoto, Japan", "Bologna, Italy", "Prague, Czechia"}
)
