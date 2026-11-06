package scanner

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseFilename(t *testing.T) {
	tests := []struct {
		path string
		want Parsed
	}{
		{"Nature/Planet Earth II - Islands (2016).mkv", Parsed{Title: "Planet Earth II - Islands", Year: 2016}},
		{"The.Matrix.1999.1080p.BluRay.x264.mkv", Parsed{Title: "The Matrix", Year: 1999}},
		{"Some_Show_S02E05_720p_WEB-DL.mp4", Parsed{Title: "Some Show S02E05", Season: 2, Episode: 5}},
		{"Show Name 1x03.avi", Parsed{Title: "Show Name 1x03", Season: 1, Episode: 3}},
		{"1917.mp4", Parsed{Title: "1917"}},
		{"2001 A Space Odyssey (1968).mkv", Parsed{Title: "2001 A Space Odyssey", Year: 1968}},
		{"[Group] Concert Live at Montreux [1080p].mp4", Parsed{Title: "Concert Live at Montreux"}},
		{"montreux_2019_set.mp4", Parsed{Title: "montreux", Year: 2019}},
		{"My home video.mov", Parsed{Title: "My home video"}},
		{"Spider-Man Homecoming 2017.mkv", Parsed{Title: "Spider-Man Homecoming", Year: 2017}},
		{"The Latest Thing.mp4", Parsed{Title: "The Latest Thing"}},

		// Names from video downloaders and upload sites.
		{"YTDown.com_YouTube_Media_UTyk0QyT0_Y_Honeymoon-Latest-Yoruba-Movies-2026-Lateef-Adedimeji-Irewole-Olaniyan-Temitope-A_003_480p.mp4",
			Parsed{Title: "Honeymoon", Year: 2026}},
		{"YTDown.com_YouTube_Media_LbD8leFCB-I_World-s-Most-Advanced-Supercars_002_720p.mp4", Parsed{Title: "World's Most Advanced Supercars"}},
		{"YTDown.com_YouTube_Media_Y07j3hXAI-g_Dangerous-apps-In-the-web-of-data-brokers-DW-Documentary_002_720p.mp4",
			Parsed{Title: "Dangerous apps In the web of data brokers"}},
		{"YTDown.com_YouTube_Media_ku9edEKvGuY_Anonymous-The-Hacker-Wars-Full-Documentary_001_720p.mp4", Parsed{Title: "Anonymous The Hacker Wars"}},
		{"YTDown.com_YouTube_Media_aEB-ppoz5iA_Celestial-Oldies-Medley_002_720p(1).mp4", Parsed{Title: "Celestial Oldies Medley"}},
		{"THE-LAST-DRIVER-LATEST-YORUBA-MOVIE-2026-BABATUNDE-ADERINOYE-IREWOLE-OLANIYAN-AB_002_720p.mp4", Parsed{Title: "THE LAST DRIVER", Year: 2026}},
		{"Kemi-Eyinju-CCC-Hymns-Worship-Easter-Edition_001_720p.mp4", Parsed{Title: "Kemi Eyinju CCC Hymns Worship Easter Edition"}},
		{"Asorokele-Latest-SnapYT.App.mp4", Parsed{Title: "Asorokele"}},
		{"Bola-Sunmisola-SnapYT.App.mp4", Parsed{Title: "Bola Sunmisola"}},
		{"ALAGBARA AYE - Full Yoruba Movie 2025 ｜ Latest Nigerian Movie Odunlade Adekola, Bose Akinola.mp4", Parsed{Title: "ALAGBARA AYE", Year: 2025}},
		{"CAFE EDEBIRI (LATEST NOLLYWOOD MOVIE)- NKEM OWOH, BOLAJI OGUNMOLA.mp4", Parsed{Title: "CAFE EDEBIRI"}},
		{"OMOTENA (His Woman) -Nigerian Movies latest Full Movies 2025 Deborah Shokoya.mp4", Parsed{Title: "OMOTENA (His Woman)", Year: 2025}},
		{"Humanoid robots and new chips - Will Germany become a tech hotspot? | DW Documentary 2026.mp4",
			Parsed{Title: "Humanoid robots and new chips - Will Germany become a tech hotspot?", Year: 2026}},
		{"24kGoldn Mood Official Video ft iann dior.mp4", Parsed{Title: "24kGoldn Mood ft iann dior"}},
		{"Falz ft Simi - Foreign.mp4", Parsed{Title: "Falz ft Simi - Foreign"}},
	}
	for _, tt := range tests {
		if got := ParseFilename(tt.path); got != tt.want {
			t.Errorf("ParseFilename(%q) = %+v, want %+v", tt.path, got, tt.want)
		}
	}
}

func TestFolderTags(t *testing.T) {
	tests := map[string][]string{
		"x.mkv":                      nil,
		"Docs/Nature/x.mkv":          {"Docs", "Nature"},
		"Shows/Lost/Season 1/e1.mkv": {"Shows", "Lost"},
	}
	for path, want := range tests {
		if got := FolderTags(path); !reflect.DeepEqual(got, want) {
			t.Errorf("FolderTags(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestContentHash(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	big := make([]byte, 5*chunk)
	for i := range big {
		big[i] = byte(i * 7)
	}
	a := write("a.mkv", big)
	b := write("renamed.mkv", big) // same content, different name

	ha, size, err := ContentHash(a)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(big)) || !strings.HasPrefix(ha, "ph1:") || len(ha) != 20 {
		t.Fatalf("unexpected hash %q size %d", ha, size)
	}
	if hb, _, _ := ContentHash(b); hb != ha {
		t.Errorf("same content, different hash: %s vs %s", ha, hb)
	}

	// The middle isn't hashed, by design: only size + first/last MiB.
	mid := append([]byte(nil), big...)
	mid[len(mid)/2] ^= 0xff
	if hm, _, _ := ContentHash(write("mid.mkv", mid)); hm != ha {
		t.Errorf("middle byte change should not affect ph1")
	}
	// A change in the last MiB must.
	end := append([]byte(nil), big...)
	end[len(end)-10] ^= 0xff
	if he, _, _ := ContentHash(write("end.mkv", end)); he == ha {
		t.Errorf("change in last MiB did not change hash")
	}
	// Small files are hashed whole.
	small := []byte("tiny video")
	h1, _, _ := ContentHash(write("s1.mp4", small))
	h2, _, _ := ContentHash(write("s2.mp4", []byte("tiny videO")))
	if h1 == h2 {
		t.Errorf("small files with different content share a hash")
	}
}

func TestPlaybackMode(t *testing.T) {
	audio := func(c string) []Stream { return []Stream{{Type: "audio", Codec: c}} }
	tests := []struct {
		p    Probe
		want string
	}{
		{Probe{Container: "mp4", VideoCodec: "h264", Streams: audio("aac")}, "direct"},
		{Probe{Container: "matroska", VideoCodec: "h264", Streams: audio("aac")}, "remux"},
		{Probe{Container: "mp4", VideoCodec: "hevc", Streams: audio("aac")}, "transcode"},
		{Probe{Container: "mp4", VideoCodec: "h264", Streams: audio("dts")}, "remux"},
		{Probe{Container: "matroska", VideoCodec: "hevc", Streams: audio("aac")}, "transcode"},
		{Probe{Container: "webm", VideoCodec: "vp9", Streams: audio("opus")}, "direct"},
	}
	for _, tt := range tests {
		if got := PlaybackMode(&tt.p); got != tt.want {
			t.Errorf("PlaybackMode(%+v) = %s, want %s", tt.p, got, tt.want)
		}
	}
}

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		path string
		want Episode
		ok   bool
	}{
		{"Breaking.Bad.S01E02.Cats.in.the.Bag.720p.WEB-DL.mkv", Episode{"Breaking Bad", 1, 2, "Cats in the Bag"}, true},
		{"TV/The Office (US)/Season 2/The Office - 2x05 - Halloween.mp4", Episode{"The Office", 2, 5, "Halloween"}, true},
		{"Planet Earth II/S01E03.mkv", Episode{"Planet Earth II", 1, 3, ""}, true},
		{"Shows/Dark/Season 3/04 - The Origin.mkv", Episode{"Dark", 3, 4, "The Origin"}, true},
		{"Shows/Dark/Season 3/Episode 7.mp4", Episode{"Dark", 3, 7, ""}, true},
		{"Shows/Dark/S2/E01 Beginnings and Endings.mp4", Episode{"Dark", 2, 1, "Beginnings and Endings"}, true},
		{"Lost (2004) S01E01 Pilot.avi", Episode{"Lost", 1, 1, "Pilot"}, true},
		// Not episodes by name alone.
		{"Movies/Inception (2010).mkv", Episode{}, false},
		{"Season 1/03.mkv", Episode{}, false}, // no show folder
		{"Concerts/Live Set 2019.mp4", Episode{}, false},
		{"Shows/Dark/Season 1/2001 A Space Odyssey.mkv", Episode{}, false},
	}
	for _, c := range cases {
		got, ok := ParseEpisode(c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ParseEpisode(%q) = %+v, %v; want %+v, %v", c.path, got, ok, c.want, c.ok)
		}
	}
}
