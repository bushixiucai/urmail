// Command i18ncheck verifies that every locale in the message catalogue has
// the same set of keys.
//
// It is the check that keeps the frontend and the backend in step: the
// backend renders messages from the same catalogue the frontend downloads, so
// a key that exists in one locale but not another would show up as a raw
// identifier to a reader in that language.
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/bushixiucai/urmail/internal/i18n"
)

func main() {
	store, err := i18n.NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "i18ncheck: %v\n", err)
		os.Exit(1)
	}

	locales := store.Locales()
	if len(locales) == 0 {
		fmt.Fprintln(os.Stderr, "i18ncheck: no locales found")
		os.Exit(1)
	}

	reference := store.Keys()
	referenceSet := make(map[string]bool, len(reference))
	for _, key := range reference {
		referenceSet[key] = true
	}

	failed := false
	for _, locale := range locales {
		messages := store.Raw(locale)

		missing := make([]string, 0)
		for _, key := range reference {
			if _, ok := messages[key]; !ok {
				missing = append(missing, key)
			}
		}
		extra := make([]string, 0)
		for key := range messages {
			if !referenceSet[key] {
				extra = append(extra, key)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)

		for _, key := range missing {
			fmt.Printf("%s: missing key %q\n", locale, key)
			failed = true
		}
		for _, key := range extra {
			fmt.Printf("%s: unknown key %q\n", locale, key)
			failed = true
		}
	}

	if failed {
		fmt.Fprintf(os.Stderr, "i18ncheck: the locales disagree; %d key(s) checked\n", len(reference))
		os.Exit(1)
	}

	fmt.Printf("i18ncheck: %d locales, %d keys, all consistent\n", len(locales), len(reference))
}
