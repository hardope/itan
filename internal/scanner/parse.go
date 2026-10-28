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
)

// ParseFilename guesses a title, year and episode numbers from a file path.
func ParseFilename(path string) Parsed {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	name = bracketed.ReplaceAllString(name, " ")

	// "Some.Movie.Name.2010" style: dots/underscores act as spaces.
	if !strings.Contains(name, " ") || strings.Count(name, ".")+strings.Count(name, "_") > strings.Count(name, " ") {
		name = strings.NewReplacer(".", " ", "_", " ").Replace(name)
	}

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
	// Pad so a year at the very start/end still has a delimiter around it.
	padded := " " + name[:cut] + " "
	if ms := yearRe.FindAllStringSubmatchIndex(padded, -1); len(ms) > 0 {
		// Use the last year-like number: "2001 A Space Odyssey (1968)" -> 1968.
		m := ms[len(ms)-1]
		start := m[2] - 1 // back to unpadded coordinates
		if start > 0 {    // a year alone is more likely the title, e.g. "1917"
			p.Year, _ = strconv.Atoi(padded[m[2]:m[3]])
			cut = min(cut, start)
		}
	}

	title := strings.Trim(name[:cut], " -–(")
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

var seasonDir = regexp.MustCompile(`(?i)^(season|series|s)\s*\d+$`)
