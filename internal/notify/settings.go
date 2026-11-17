package notify

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // the distroless image has no zoneinfo; the digest time zone must still resolve
)

// Settings keys (ordinary settings: restored with a backup). The state notifications keep between
// checks is in the notify_* tables, which aren't.
const (
	KeyDigest       = "notifyDigest"
	KeyCritical     = "notifyCritical"
	KeyUnrecognized = "notifyUnrecognized"
	KeyNewCluster   = "notifyNewCluster"
	KeyDigestDay    = "notifyDigestDay"
	KeyDigestTime   = "notifyDigestTime"
	KeyDigestTZ     = "notifyDigestTZ"
)

// Settings are edited in Settings → Notifications. Every kind is off by default.
type Settings struct {
	Digest       bool   `json:"digest"`
	Critical     bool   `json:"critical"`
	Unrecognized bool   `json:"unrecognized"`
	NewCluster   bool   `json:"newCluster"`
	DigestDay    string `json:"digestDay"`  // monday … sunday
	DigestTime   string `json:"digestTime"` // HH:MM
	DigestTZ     string `json:"digestTimeZone"`
}

var days = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

func Defaults() Settings {
	return Settings{DigestDay: "monday", DigestTime: "09:00", DigestTZ: "Europe/Belgrade"}
}

func ParseSettings(m map[string]string) Settings {
	s := Defaults()
	s.Digest, s.Critical = m[KeyDigest] == "on", m[KeyCritical] == "on"
	s.Unrecognized, s.NewCluster = m[KeyUnrecognized] == "on", m[KeyNewCluster] == "on"
	if dayIndex(m[KeyDigestDay]) >= 0 {
		s.DigestDay = m[KeyDigestDay]
	}
	if _, _, ok := parseHM(m[KeyDigestTime]); ok {
		s.DigestTime = m[KeyDigestTime]
	}
	if _, err := loadTZ(m[KeyDigestTZ]); err == nil {
		s.DigestTZ = m[KeyDigestTZ]
	}
	return s
}

// Normalize validates and canonicalises (day lowercase, time zero-padded).
func (s *Settings) Normalize() error {
	s.DigestDay = strings.ToLower(strings.TrimSpace(s.DigestDay))
	if dayIndex(s.DigestDay) < 0 {
		return errors.New("digestDay: monday … sunday")
	}
	h, m, ok := parseHM(s.DigestTime)
	if !ok {
		return errors.New("digestTime: HH:MM, e.g. 09:00")
	}
	s.DigestTime = fmt.Sprintf("%02d:%02d", h, m)
	s.DigestTZ = strings.TrimSpace(s.DigestTZ)
	if _, err := loadTZ(s.DigestTZ); err != nil || len(s.DigestTZ) > 64 {
		return errors.New("digestTimeZone: an IANA time zone, e.g. Europe/Belgrade or UTC")
	}
	return nil
}

func (s Settings) Values() map[string]string {
	on := func(b bool) string {
		if b {
			return "on"
		}
		return ""
	}
	return map[string]string{KeyDigest: on(s.Digest), KeyCritical: on(s.Critical), KeyUnrecognized: on(s.Unrecognized),
		KeyNewCluster: on(s.NewCluster), KeyDigestDay: s.DigestDay, KeyDigestTime: s.DigestTime, KeyDigestTZ: s.DigestTZ}
}

func dayIndex(d string) int {
	for i, x := range days {
		if x == d {
			return i
		}
	}
	return -1
}

func parseHM(v string) (int, int, bool) {
	hs, ms, ok := strings.Cut(v, ":")
	if !ok {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 || len(ms) != 2 {
		return 0, 0, false
	}
	return h, m, true
}

func loadTZ(name string) (*time.Location, error) {
	if name == "" || name == "Local" { // "" would be UTC, "Local" the server's zone: neither is a choice
		return nil, errors.New("invalid time zone")
	}
	return time.LoadLocation(name)
}

// LastSlot is the most recent scheduled digest time at or before now.
func (s Settings) LastSlot(now time.Time) time.Time {
	loc, err := loadTZ(s.DigestTZ)
	if err != nil {
		loc = time.UTC
	}
	h, m, _ := parseHM(s.DigestTime)
	local := now.In(loc)
	t := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, loc)
	back := (int(local.Weekday()) - dayIndex(s.DigestDay) + 7) % 7
	t = t.AddDate(0, 0, -back)
	if t.After(now) {
		t = t.AddDate(0, 0, -7)
	}
	return t
}

// NextSlot is the first scheduled time after now.
func (s Settings) NextSlot(now time.Time) time.Time {
	t := s.LastSlot(now)
	return time.Date(t.Year(), t.Month(), t.Day()+7, t.Hour(), t.Minute(), 0, 0, t.Location())
}
