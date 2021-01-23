package pwd

import (
	"bytes"
	"fmt"

	"github.com/chzyer/readline"
	"github.com/fatih/color"

	zxcvbn "github.com/nbutton23/zxcvbn-go"
	"github.com/sahib/brig/util"
)

const (
	msgLowEntropy  = "Please enter a password with at least %g bits entropy."
	msgReEnter     = "Well done! Please re-type your password now for safety:"
	msgBadPassword = "This did not seem to match. Please retype it again."
	msgMaxTriesHit = "Maximum number of password tries exceeded: %d"
)

func doPromptLine(rl *readline.Instance, prompt string, hide bool) ([]byte, error) {
	var line []byte
	var sline string
	var err error

	if hide {
		line, err = rl.ReadPassword(prompt)
	} else {
		sline, err = rl.Readline()
		line = []byte(sline)
	}

	if err != nil {
		return nil, err
	}

	return line, nil
}

func createStrengthPrompt(password []rune, prefix string) string {
	var symbol string
	var colorFn func(format string, a ...interface{}) string

	strength := zxcvbn.PasswordStrength(string(password), nil)

	switch {
	case strength.Entropy >= 25:
		symbol = "✔"
		colorFn = color.GreenString
	case strength.Entropy >= 20:
		symbol = "⊞"
		colorFn = color.YellowString
	case strength.Entropy >= 15:
		symbol = "⊟"
		colorFn = color.MagentaString
	default:
		symbol = "⊠"
		colorFn = color.RedString
	}

	return colorFn(symbol + "  " + prefix + "passphrase: ")
}

// PromptNewPassword asks the user to input a password.
//
// While typing, the user gets feedback by the prompt color,
// which changes with the security of the password to green.
// Additionally the entrtopy of the password is shown.
// If minEntropy was not reached after hitting enter,
// this function will log a message and ask the user again.
func PromptNewPassword(minEntropy float64) ([]byte, error) {
	rl, err := readline.New("")
	if err != nil {
		return nil, err
	}
	defer util.Closer(rl)

	passwordCfg := rl.GenPasswordConfig()
	passwordCfg.SetListener(func(line []rune, pos int, key rune) (newLine []rune, newPos int, ok bool) {
		rl.SetPrompt(createStrengthPrompt(line, "New "))
		rl.Refresh()
		return nil, 0, false
	})

	pwd := []byte{}

	for {
		pwd, err = rl.ReadPasswordWithConfig(passwordCfg)
		if err != nil {
			return nil, err
		}

		strength := zxcvbn.PasswordStrength(string(pwd), nil)
		if strength.Entropy >= minEntropy {
			break
		}

		fmt.Printf(color.YellowString(msgLowEntropy)+"\n", minEntropy)
	}

	passwordCfg.SetListener(func(line []rune, pos int, key rune) (newLine []rune, newPos int, ok bool) {
		rl.SetPrompt(createStrengthPrompt(line, "Retype "))
		rl.Refresh()
		return nil, 0, false
	})

	fmt.Println(msgReEnter)

	for {
		newPwd, err := rl.ReadPasswordWithConfig(passwordCfg)
		if err != nil {
			return nil, err
		}

		if bytes.Equal(pwd, newPwd) {
			break
		}

		fmt.Println(color.YellowString(msgBadPassword))
	}

	strength := zxcvbn.PasswordStrength(string(pwd), nil)
	fmt.Printf(
		"estimated time needed to crack password (according to zxcvbn): %s\n",
		color.BlueString(strength.CrackTimeDisplay),
	)

	return pwd, nil
}

func promptPassword(prompt string) ([]byte, error) {
	rl, err := readline.New(prompt)
	if err != nil {
		return nil, err
	}

	defer util.Closer(rl)
	return doPromptLine(rl, prompt, true)
}

// PromptPassword just opens an uncolored password prompt.
//
// The password is not echo'd to stdout for safety reasons.
func PromptPassword() ([]byte, error) {
	return promptPassword("Password: ")
}
