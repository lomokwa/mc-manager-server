package automation

import (
	"strings"
	"testing"
)

func TestInterpolate_ReplacesKnownVariables(t *testing.T) {
	got := Interpolate("{server} caiu às {time}", map[string]string{
		"server": "default", "time": "12:00:00",
	}, false)
	if got != "default caiu às 12:00:00" {
		t.Errorf("got %q", got)
	}
}

// An unknown variable stays literal rather than becoming an empty string: a
// typo should look like a typo, not like missing data.
func TestInterpolate_LeavesUnknownVariablesAlone(t *testing.T) {
	got := Interpolate("{server} {nope}", map[string]string{"server": "default"}, false)
	if got != "default {nope}" {
		t.Errorf("got %q", got)
	}
}

// THE bug this file exists for. {line} and {player} carry text a player wrote
// when the trigger is a chat message. Interpolated into a console command, a
// newline in that text turns their chat into a second command.
func TestInterpolate_CollapsesNewlinesForCommands(t *testing.T) {
	hostile := map[string]string{"player": "Ant\nop Ant\r\ndeop Notch\rgamemode creative"}

	forCmd := Interpolate("say olá {player}", hostile, true)
	if strings.ContainsAny(forCmd, "\r\n") {
		t.Errorf("a command must never carry a line break, got %q", forCmd)
	}

	// A Discord message is text, not an instruction -- newlines belong there.
	forMsg := Interpolate("olá {player}", hostile, false)
	if !strings.Contains(forMsg, "\n") {
		t.Error("a Discord message should keep the original text intact")
	}
}

// Collapsing must not silently glue words together, or a moderator reading
// the console sees "opAnt" and cannot tell what happened.
func TestInterpolate_CollapsedBreaksBecomeSpaces(t *testing.T) {
	got := Interpolate("say {line}", map[string]string{"line": "primeira\nsegunda"}, true)
	if got != "say primeira segunda" {
		t.Errorf("expected the break to become a space, got %q", got)
	}
}

func TestValidateAction_RefusesLineInsideACommand(t *testing.T) {
	err := ValidateAction(Action{Type: "command", Command: "say vi seu pedido: {line}"})
	if err == nil {
		t.Fatal("expected {line} to be refused inside a command action")
	}
	if !strings.Contains(err.Error(), "{line}") {
		t.Errorf("the error must name the offending variable, got %q", err)
	}
}

func TestValidateAction_AllowsLineInADiscordMessage(t *testing.T) {
	if err := ValidateAction(Action{Type: "discord", WebhookID: 1, Message: "chat: {line}"}); err != nil {
		t.Errorf("{line} is text in a Discord message, not an instruction: %v", err)
	}
}

func TestValidateAction_RejectsAnEmptyCommandAndAMissingWebhook(t *testing.T) {
	if err := ValidateAction(Action{Type: "command", Command: "   "}); err == nil {
		t.Error("expected an empty command to be rejected")
	}
	if err := ValidateAction(Action{Type: "discord", Message: "oi"}); err == nil {
		t.Error("expected a Discord action without a webhook to be rejected")
	}
	if err := ValidateAction(Action{Type: "discord", WebhookID: 1, Message: "  "}); err == nil {
		t.Error("expected a Discord action without a message to be rejected")
	}
}

func TestValidateAction_RejectsNonsenseRestartWarnings(t *testing.T) {
	if err := ValidateAction(Action{Type: "restart", Warnings: []int{60, 0}}); err == nil {
		t.Error("a zero-second warning is not a warning")
	}
	if err := ValidateAction(Action{Type: "restart", Warnings: []int{-5}}); err == nil {
		t.Error("a negative warning must be rejected")
	}
	if err := ValidateAction(Action{Type: "restart", Warnings: []int{300, 60, 15}}); err != nil {
		t.Errorf("a normal warning sequence must be accepted: %v", err)
	}
	// No warnings at all is legitimate: "restart now" is a real thing to want.
	if err := ValidateAction(Action{Type: "restart"}); err != nil {
		t.Errorf("a restart with no warnings must be allowed: %v", err)
	}
}

func TestValidateAction_RejectsAnUnknownType(t *testing.T) {
	if err := ValidateAction(Action{Type: "rm -rf"}); err == nil {
		t.Error("an unknown action type must be rejected rather than silently ignored")
	}
}

// A command carrying its own literal newline is the same attack without the
// variable. Validation has to catch it at save time, not rely on
// interpolation to clean up something that was never a variable.
func TestValidateAction_RefusesALiteralNewlineInACommand(t *testing.T) {
	if err := ValidateAction(Action{Type: "command", Command: "say oi\nop Ant"}); err == nil {
		t.Error("a command containing a literal newline must be refused")
	}
}
