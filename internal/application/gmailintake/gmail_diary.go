package gmailintake

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

var gmailDiarySubjectPattern = regexp.MustCompile(`^【ルミナとれんの会話記録】([0-9]{4}-[0-9]{2}-[0-9]{2})$`)

type parsedGmailDiary struct{ Date, Body, Reflection string }

// parseGmailDiary accepts only the three top-level headings in the agreed
// mail format. It never interprets quoted text, code blocks, or nested lists.
func parseGmailDiary(message GmailMessage) (parsedGmailDiary, error) {
	match := gmailDiarySubjectPattern.FindStringSubmatch(strings.TrimSpace(message.Subject))
	if len(match) != 2 {
		return parsedGmailDiary{}, errors.New("diary subject does not match the exact format")
	}
	date, err := time.Parse("2006-01-02", match[1])
	if err != nil || date.Format("2006-01-02") != match[1] {
		return parsedGmailDiary{}, errors.New("diary subject date is invalid")
	}
	expectedJapaneseDate := fmt.Sprintf("%d年%d月%d日 今日の会話", date.Year(), int(date.Month()), date.Day())
	expectedPaddedJapaneseDate := fmt.Sprintf("%d年%02d月%02d日 今日の会話", date.Year(), int(date.Month()), date.Day())
	from, err := mail.ParseAddress(strings.TrimSpace(message.From))
	if err != nil || !strings.EqualFold(from.Address, "nyukimi@gmail.com") {
		return parsedGmailDiary{}, errors.New("diary sender does not match the configured author")
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(message.ReceivedAt)); err != nil {
		return parsedGmailDiary{}, errors.New("diary received timestamp is invalid")
	}
	if len(message.Text) == 0 || len(message.Text) > maxGmailTextBytes {
		return parsedGmailDiary{}, errors.New("diary body is empty or exceeds the source bound")
	}
	type section struct{ start, contentStart, end int }
	sections := map[int]section{}
	current := 0
	var lines []string
	fenceMarker := byte(0)
	fenceLength := 0
	for _, line := range strings.Split(message.Text, "\n") {
		trimmed := strings.TrimSpace(line)
		if fenceMarker != 0 {
			if current != 0 {
				lines = append(lines, line)
			}
			if closesDiaryFence(trimmed, fenceMarker, fenceLength) {
				fenceMarker, fenceLength = 0, 0
			}
			continue
		}
		if marker, length, ok := opensDiaryFence(trimmed); ok {
			if current != 0 {
				lines = append(lines, line)
			}
			fenceMarker, fenceLength = marker, length
			continue
		}
		if strings.HasPrefix(trimmed, ">") || strings.HasPrefix(line, "\t") {
			if current != 0 {
				lines = append(lines, line)
			}
			continue
		}
		if n, ok := diaryHeadingNumber(line, expectedJapaneseDate, expectedPaddedJapaneseDate); ok {
			if n != current+1 {
				return parsedGmailDiary{}, errors.New("diary sections are missing or out of order")
			}
			if current != 0 {
				sec := sections[current]
				sec.end = len(lines)
				sections[current] = sec
			}
			current = n
			if _, duplicate := sections[n]; duplicate {
				return parsedGmailDiary{}, fmt.Errorf("diary section %d is duplicated", n)
			}
			sections[n] = section{start: len(lines), end: -1}
			lines = append(lines, line)
			sectionValue := sections[n]
			sectionValue.contentStart = len(lines)
			sections[n] = sectionValue
			continue
		}
		if current != 0 {
			lines = append(lines, line)
		}
	}
	if fenceMarker != 0 {
		return parsedGmailDiary{}, errors.New("diary body has an unclosed code fence")
	}
	if current != 0 {
		sec := sections[current]
		sec.end = len(lines)
		sections[current] = sec
	}
	for i := 1; i <= 3; i++ {
		if _, ok := sections[i]; !ok {
			return parsedGmailDiary{}, fmt.Errorf("diary section %d is missing", i)
		}
	}
	part := func(n int) string {
		sec := sections[n]
		if sec.end < sec.start || sec.end > len(lines) {
			return ""
		}
		return strings.TrimSpace(strings.Join(lines[sec.start:sec.end], "\n"))
	}
	content := func(n int) string {
		sec := sections[n]
		if sec.end < sec.contentStart || sec.end > len(lines) {
			return ""
		}
		return strings.TrimSpace(strings.Join(lines[sec.contentStart:sec.end], "\n"))
	}
	conversation, summary, reflection := part(1), part(2), part(3)
	if conversation == "" || summary == "" || reflection == "" || content(1) == "" || content(2) == "" || content(3) == "" {
		return parsedGmailDiary{}, errors.New("diary section is empty")
	}
	return parsedGmailDiary{Date: match[1], Body: conversation + "\n\n" + summary, Reflection: reflection}, nil
}

func opensDiaryFence(line string) (byte, int, bool) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, false
	}
	marker := line[0]
	n := 0
	for n < len(line) && line[n] == marker {
		n++
	}
	return marker, n, n >= 3
}

func closesDiaryFence(line string, marker byte, minimum int) bool {
	n := 0
	for n < len(line) && line[n] == marker {
		n++
	}
	return n >= minimum && strings.TrimSpace(line[n:]) == ""
}

func diaryHeadingNumber(line string, japaneseDateHeadings ...string) (int, bool) {
	if strings.HasPrefix(strings.TrimLeft(line, " "), "#") {
		leading := len(line) - len(strings.TrimLeft(line, " "))
		if leading > 3 {
			return 0, false
		}
		line = strings.TrimLeft(line, " ")
		headingMarks := len(line) - len(strings.TrimLeft(line, "#"))
		if headingMarks < 1 || headingMarks > 6 || (len(line) > headingMarks && line[headingMarks] != ' ' && line[headingMarks] != '\t') {
			return 0, false
		}
		line = strings.TrimSpace(line[headingMarks:])
	} else if strings.TrimSpace(line) != line {
		return 0, false
	}
	line = strings.TrimSpace(line)
	for number, title := range map[int]string{1: "今日の会話", 2: "今日のまとめ", 3: "ルミナの感想"} {
		if line == title {
			return number, true
		}
		if number == 1 {
			for _, dated := range japaneseDateHeadings {
				if line == dated {
					return number, true
				}
			}
		}
		for _, prefix := range []string{fmt.Sprintf("%d. %s", number, title), fmt.Sprintf("%d) %s", number, title)} {
			if line == prefix || line == "**"+prefix+"**" {
				return number, true
			}
		}
	}
	return 0, false
}
