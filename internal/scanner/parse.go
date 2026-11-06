package scanner

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Parsed is what can be guessed from a file name alone.
type Parsed struct {
	Title   string
	Year    int
	Season  int
	Episode int
}

var (
	bracketed = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	yearRe    = regexp.MustCompile(`[\(\s]((?:19|20)\d{2})[\)\s]`)
	episodeRe = regexp.MustCompile(`(?i)\bS(\d{1,2})[\s.]?E(\d{1,3})\b|\b(\d{1,2})x(\d{2,3})\b`)
	// Release junk: everything from the first of these onward is dropped.
	junkRe = regexp.MustCompile(`(?i)\b(2160p|1080p|1080i|720p|576p|480p|4k|uhd|hdr|hdr10|dv|` +
		`x264|x265|h\.?264|h\.?265|hevc|av1|xvid|divx|10bit|` +
		`bluray|blu-ray|bdrip|brrip|web-?dl|web-?rip|webrip|hdtv|dvdrip|hdrip|remux|` +
		`aac|ac3|dts|ddp?5\.1|atmos|proper|repack|extended|unrated)\b`)
	spaces = regexp.MustCompile(`\s+`)

	// Names video downloaders give files: "YTDown.com_YouTube_Media_<video
	// id>_Title_002_720p", "Title-SnapYT.App", "y2mate.com - Title", and
	// "(1)" on a second copy.
	downloaderPrefix = regexp.MustCompile(`(?i)^(?:ytdown\.com_youtube_media_[a-z0-9_-]{11}_|` +
		`(?:y2mate\.(?:com|is)|ssyoutube\.com|savefrom\.net|yt1s\.com|ytmp3\.cc|snapyt\.app)\s*[-_ ]\s*)`)
	downloaderSuffix = regexp.MustCompile(`(?i)\s*\(\d{1,2}\)$|_\d{3}_(?:\d{3,4}p|4k)$|(?:[-_ ]+snapyt)?\.app$|[-_ ]+savefrom\.net$`)
	// Words joined by dashes, "The-Last-Driver" (not "Spider-Man"), with
	// apostrophes the downloader turned into dashes: "World-s", "Don-t".
	dashedWords = regexp.MustCompile(`\S+(?:-\S+){2,}`)
	dashedS     = regexp.MustCompile(`([A-Za-z])-s-`)
	dashedNT    = regexp.MustCompile(`([A-Za-z])n-t-`)
	officialRe  = regexp.MustCompile(`(?i)\(\s*official[^)]*\)|\bofficial\s+(?:music\s+|lyrics?\s+)?(?:video|audio)\b`)
	// What upload sites put after a title: "Title - Latest Yoruba Movie 2025
	// <cast>", "Title | DW Documentary". The title ends at the first of
	// these; a year in the rest still counts. "Latest" only when a movie or
	// a year follows soon, or it ends the name.
	paddingRe = regexp.MustCompile(`(?i)\s[|｜]|` +
		`\blatest\b(?:(?:\s+\S+){0,3}?\s+(?:movies?|film|(?:19|20)\d{2})\b|\s*\)?$)|` +
		`\bfull\s+(?:yoruba\s+|nigerian\s+|nollywood\s+|ghanaian\s+)?movies?\b|` +
		`\b(?:yoruba|nigerian|nollywood|ghanaian)\s+movies?\b|` +
		`\b(?:full|dw|bbc|free)\s+documentar(?:y|ies)\b|\bexclusive\s*$`)
)

// cleanName returns a file's name without extension, bracketed tags and
// what downloaders add, with dots, underscores and dashes read as spaces
// when they separate words.
func cleanName(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for range 3 { // "Title_002_720p(1)": suffixes stack
		name = downloaderSuffix.ReplaceAllString(name, "")
	}
	name = downloaderPrefix.ReplaceAllString(name, "")
	name = officialRe.ReplaceAllString(name, " ")
	name = bracketed.ReplaceAllString(name, " ")
	// "Some.Movie.Name.2010" style: dots/underscores act as spaces.
	if !strings.Contains(name, " ") || strings.Count(name, ".")+strings.Count(name, "_") > strings.Count(name, " ") {
		name = strings.NewReplacer(".", " ", "_", " ").Replace(name)
	}
	if !strings.Contains(name, " ") { // "Bola-Sunmisola"
		name = undash(name)
	}
	name = dashedWords.ReplaceAllStringFunc(name, undash)
	return spaces.ReplaceAllString(strings.TrimSpace(name), " ")
}

// undash reads the dashes in "World-s-Most-Advanced" as spaces, and the
// ones that stood for apostrophes as apostrophes.
func undash(w string) string {
	w = dashedS.ReplaceAllString(w+"-", "$1's-")
	w = dashedNT.ReplaceAllString(w, "${1}n't-")
	return strings.TrimSpace(strings.ReplaceAll(w, "-", " "))
}

// ParseFilename guesses a title, year and episode numbers from a file path.
func ParseFilename(path string) Parsed {
	name := cleanName(path)

	var p Parsed
	cut := len(name)

	if m := junkRe.FindStringIndex(name); m != nil && m[0] > 0 {
		cut = m[0]
	}
	if m := episodeRe.FindStringSubmatchIndex(name); m != nil {
		sub := episodeRe.FindStringSubmatch(name)
		if sub[1] != "" {
			p.Season, _ = strconv.Atoi(sub[1])
			p.Episode, _ = strconv.Atoi(sub[2])
		} else {
			p.Season, _ = strconv.Atoi(sub[3])
			p.Episode, _ = strconv.Atoi(sub[4])
		}
		// Keep the episode marker in the title so episodes stay distinguishable.
		cut = min(cut, m[1])
	}
	end := cut // where the title ends
	if m := paddingRe.FindStringIndex(name[:cut]); m != nil && m[0] > 0 {
		end = m[0]
	}
	// Pad so a year at the very start/end still has a delimiter around it.
	padded := " " + name[:end] + " "
	if ms := yearRe.FindAllStringSubmatchIndex(padded, -1); len(ms) > 0 {
		// Use the last year-like number: "2001 A Space Odyssey (1968)" -> 1968.
		m := ms[len(ms)-1]
		start := m[2] - 1 // back to unpadded coordinates
		if start > 0 {    // a year alone is more likely the title, e.g. "1917"
			p.Year, _ = strconv.Atoi(padded[m[2]:m[3]])
			end = min(end, start)
		}
	} else if end < cut {
		// "Title - Latest Yoruba Movie 2025": the year is in the padding.
		if m := yearRe.FindStringSubmatch(" " + name[end:cut] + " "); m != nil {
			p.Year, _ = strconv.Atoi(m[1])
		}
	}

	title := strings.Trim(name[:end], " -–(|｜;,")
	title = spaces.ReplaceAllString(title, " ")
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	p.Title = title
	return p
}

// FolderTags turns the directories between the library root and the file
// into tags: "Nature/Oceans/x.mkv" -> ["Nature", "Oceans"].
func FolderTags(relPath string) []string {
	dir := filepath.Dir(relPath)
	if dir == "." {
		return nil
	}
	var tags []string
	for _, part := range strings.Split(filepath.ToSlash(dir), "/") {
		// Season folders are structure, not topics.
		if part == "" || seasonDir.MatchString(part) {
			continue
		}
		tags = append(tags, part)
	}
	return tags
}

var seasonDir = regexp.MustCompile(`(?i)^(?:season|series|s)\s*(\d+)$`)

// Episode is what a path says about one episode of a series.
type Episode struct {
	Show   string // series name
	Season int    // 1 if the path doesn't say
	Number int
	Title  string // episode title, if the name has one
}

var (
	// "03 - Title", "E03 Title", "Ep 3", "Episode 3" at the start of a name.
	leadingEpisode = regexp.MustCompile(`(?i)^(?:e|ep|episode)?\s*(\d{1,3})(?:\s*[-.:]\s*|\s+|$)`)
	trailingYear   = regexp.MustCompile(`\s*\(?(?:19|20)\d{2}\)?$`)
)

// ParseEpisode recognises episodes in the usual layouts:
//
//	Show.S01E02.Title.mkv, Show 1x02 Title.mp4     (show from the name, or
//	                                               the folder if the name
//	                                               starts with the marker)
//	Show/Season 1/02 - Title.mkv, .../Episode 2.mp4 (show from the folder
//	                                               above the season folder)
//
// Anything else isn't an episode as far as names go; people can still put
// it in a series by hand.
func ParseEpisode(relPath string) (Episode, bool) {
	name := cleanName(relPath)

	// The closest folder that isn't a season folder names the show.
	folderShow, folderSeason := "", 0
	if dir := filepath.ToSlash(filepath.Dir(relPath)); dir != "." {
		parts := strings.Split(dir, "/")
		for i := len(parts) - 1; i >= 0; i-- {
			if m := seasonDir.FindStringSubmatch(parts[i]); m != nil {
				if folderSeason == 0 {
					folderSeason, _ = strconv.Atoi(m[1])
				}
				continue
			}
			folderShow = cleanShow(parts[i])
			break
		}
	}

	var ep Episode
	if m := episodeRe.FindStringSubmatchIndex(name); m != nil {
		sub := episodeRe.FindStringSubmatch(name)
		if sub[1] != "" {
			ep.Season, _ = strconv.Atoi(sub[1])
			ep.Number, _ = strconv.Atoi(sub[2])
		} else {
			ep.Season, _ = strconv.Atoi(sub[3])
			ep.Number, _ = strconv.Atoi(sub[4])
		}
		ep.Show = cleanShow(name[:m[0]])
		ep.Title = cleanEpisodeTitle(name[m[1]:])
	} else if folderSeason > 0 {
		m := leadingEpisode.FindStringSubmatchIndex(strings.TrimSpace(name))
		if m == nil {
			return Episode{}, false
		}
		trimmed := strings.TrimSpace(name)
		ep.Season = folderSeason
		ep.Number, _ = strconv.Atoi(trimmed[m[2]:m[3]])
		ep.Title = cleanEpisodeTitle(trimmed[m[1]:])
	} else {
		return Episode{}, false
	}
	if ep.Show == "" {
		ep.Show = folderShow
	}
	if ep.Season == 0 {
		ep.Season = max(folderSeason, 1)
	}
	if strings.EqualFold(ep.Title, ep.Show) {
		ep.Title = ""
	}
	return ep, ep.Show != "" && ep.Number > 0
}

// cleanShow tidies a show name: no year, no separators at the ends.
func cleanShow(s string) string {
	s = spaces.ReplaceAllString(strings.Trim(s, " -–:."), " ")
	if t := strings.Trim(trailingYear.ReplaceAllString(s, ""), " -–:."); t != "" {
		s = t
	}
	return s
}

// cleanEpisodeTitle drops release junk and separators after the marker.
func cleanEpisodeTitle(s string) string {
	if m := junkRe.FindStringIndex(s); m != nil {
		s = s[:m[0]]
	}
	if m := yearRe.FindStringIndex(" " + s + " "); m != nil && m[0] > 0 {
		s = s[:m[0]-1]
	}
	return spaces.ReplaceAllString(strings.Trim(s, " -–:.()"), " ")
}
