package automation

import (
	"fmt"
	"regexp"
	"strings"
)

// lineBreakCollapser turns any line break into a single space. A space, not
// nothing: gluing words together ("opAnt") would leave a moderator reading the
// console unable to tell what actually happened.
var lineBreakCollapser = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// Interpolate substitutes {name} from vars. forCommand collapses line breaks,
// because a console command is an instruction and a line break inside one
// starts a SECOND instruction.
//
// This is not hypothetical. {line} is the console line that matched, and
// {player} is a name from it; when the trigger is a chat message, that text is
// written by a player:
//
//	rule:   when line matches /socorro/ -> run command: say vi seu pedido, {line}
//	player: types  socorro\nop MeuNick
//
// The Discord bot in this same project already took this exact bite (its PR #8,
// "newline-collapse console-injection defense"). Repeating it here would be
// choosing to.
//
// An unknown {name} is left literal rather than blanked, so a typo looks like a
// typo instead of like missing data.
func Interpolate(template string, vars map[string]string, forCommand bool) string {
	// One pass over the template, not one pass per variable. Replacing in a
	// loop re-scans text that was already substituted, so a "{server}" typed by
	// a player inside {line} would expand or not depending on Go's random map
	// iteration order -- the same input producing different output on different
	// runs. A single pass makes substituted text inert by construction.
	return placeholderRe.ReplaceAllStringFunc(template, func(match string) string {
		value, ok := vars[match[1:len(match)-1]]
		if !ok {
			return match // unknown name stays literal: a typo should look like one
		}
		if forCommand {
			value = lineBreakCollapser.Replace(value)
		}
		return value
	})
}

// placeholderRe matches a {name} token. Deliberately narrow -- letters, digits
// and underscore -- so arbitrary braces in a message are left alone.
var placeholderRe = regexp.MustCompile(`\{[A-Za-z0-9_]+\}`)

// ValidateAction rejects an action that cannot be made safe. It runs when a
// rule is saved, so the author is told at write time rather than surprised at
// fire time -- on a live server, with players on it.
func ValidateAction(a Action) error {
	switch a.Type {
	case "discord":
		if a.WebhookID == 0 {
			return fmt.Errorf("a Discord action needs a webhook")
		}
		if strings.TrimSpace(a.Message) == "" {
			return fmt.Errorf("a Discord action needs a message")
		}

	case "command":
		if strings.TrimSpace(a.Command) == "" {
			return fmt.Errorf("a command action needs a command")
		}
		// The same attack without a variable: a literal break in the command
		// text. Interpolation would never see this one, so it has to be caught
		// here.
		if strings.ContainsAny(a.Command, "\r\n") {
			return fmt.Errorf("a command can't contain a line break: the server would read what follows as a second command")
		}
		// Sanitising {line} would still leave a player choosing most of the
		// command's text. Refusing it outright is the honest answer, and the
		// builder can say exactly why.
		if strings.Contains(a.Command, "{line}") {
			return fmt.Errorf("{line} can't be used in a command: it is written by a player, so it would let them choose what the server runs. Use it in a Discord message instead")
		}

	case "restart":
		for _, w := range a.Warnings {
			if w <= 0 {
				return fmt.Errorf("a restart warning must be a positive number of seconds, got %d", w)
			}
		}

	case "spark", "backup":
		// Nothing to configure, so nothing to get wrong.

	default:
		return fmt.Errorf("unknown action type %q", a.Type)
	}
	return nil
}

// ValidateActions checks a whole rule's action list, naming the position of
// whatever fails. A rule is saved as one unit, so a single bad step has to
// block the save rather than be silently dropped from an otherwise-valid rule.
func ValidateActions(actions []Action) error {
	if len(actions) == 0 {
		return fmt.Errorf("a rule needs at least one action")
	}
	for i, a := range actions {
		if err := ValidateAction(a); err != nil {
			return fmt.Errorf("action %d: %w", i+1, err)
		}
	}
	return nil
}
