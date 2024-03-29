// Program stockopt optimizes a stock sale subject to limitations of capital
// gains.  The input to the program is an .xls spreadsheet as generated from
// the Gain/Loss view of the MSSB stock plan site.
//
// The output is a table listing how many of each lot of stock should be sold,
// the total sale price based on the estimated sale values from MSSB, and the
// total capital gain from the sale.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/creachadair/stockopt/currency"
	"github.com/creachadair/stockopt/solver"
	"github.com/creachadair/stockopt/statement"
)

var (
	inputPath    = flag.String("input", "", "Input .xls file")
	ageMonths    = flag.Int("age", 12, "Minimum age in months (12 months is the short-term cutoff)")
	planFilter   = flag.String("plan", "GSU Class C", "Consider only shares issued under this plan")
	capGainLimit = flag.String("gain", "$0", "Capital gain limit in USD")
	marketPrice  = flag.String("market", "$0", "Market price override in USD")
	printSummary = flag.Bool("summary", false, "Print summary of available shares and exit")
	allowLoss    = flag.Bool("loss", false, "Allow sale of capital losses")
	taxRate      = flag.Int("tax", 20, "Capital gains tax rate (percent)")
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: %[1]s -input file.xls -summary  # summarize available shares
       %[1]s -input file.xls -gain v   # generate a sale profile

Read a MSSB gain/loss report from an .xls file and generate a stock sale
profile that maximizes total sale value for a given market price without
exceeding the specified maximum capital gain.

By default:

- The market price is derived from the value stated in the report; use -market
  to use a different value, e.g., for a limit order.

- Sales resulting in a capital loss are not considered; use -loss to allow the
  optimizer to include sales resulting in a capital loss in the plan.

- Only shares issued at least 12 months ago (the cutoff for long-term capital
  gains) are considered for sale; use -age to set a different threshold.

Use -summary to report on all available shares without generating a sale
profile.

Options:
`, filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()
	if *inputPath == "" {
		log.Fatal("You must provide an -input .xls path")
	} else if *taxRate < 0 || *taxRate > 100 {
		log.Fatal("You must provide a -tax rate between 0..100 percent")
	}

	// Convert the capital gains cap into a currency value.
	maxGain, err := currency.ParseUSD(*capGainLimit)
	if err != nil {
		log.Fatalf("Invalid cap %q: %v", *capGainLimit, err)
	}
	market, err := currency.ParseUSD(*marketPrice)
	if err != nil {
		log.Fatalf("Invalid market price %q: %v", *marketPrice, err)
	}

	// Read and parse the input spreadsheet, filtering out entries with 0
	// available shares, those issued more recently than the specified age, and
	// not matching the specified plan filter.
	data, err := os.ReadFile(*inputPath)
	if err != nil {
		log.Fatalf("Reading statement: %v", err)
	}

	then := time.Now().AddDate(0, -*ageMonths, 0)
	es, err := statement.ParseXLS(data, &statement.Options{
		Filter: func(e *statement.Entry) bool {
			return e.Available > 0 && e.Acquired.Before(then) &&
				(*planFilter == "" || e.Plan == *planFilter) &&
				(e.Gain >= 0 || *allowLoss)
		},
		MarketPrice: market,
	})
	if err != nil {
		log.Fatalf("Parsing statement: %v", err)
	}

	// Compute the total value of the portfolio, just for cosmetics.
	var totalValue, totalGain, totalBasis currency.Value
	var totalShares int
	for _, e := range es {
		totalShares += e.Available
		v := currency.Value(e.Available)
		totalValue += v * e.Price
		totalGain += v * e.Gain
		totalBasis += v * e.IssuePrice
	}

	fmt.Printf(`Input file:   %q
Minimum age:   %d months
Gains cap:     %s
Allow loss:    %v
Total shares:  %d
Cost basis:    %s
Present value: %s
Total gains:   %s
`, *inputPath, *ageMonths, maxGain.USD(), *allowLoss, totalShares,
		totalBasis.USD(), totalValue.USD(), totalGain.USD())
	if market > 0 {
		fmt.Printf("Market price:  %s\n", market.USD())
	}

	// If requested, print a summary of available shares.
	if *printSummary {
		fmt.Println("\nAvailable shares:")
		for _, e := range es {
			fmt.Printf("%2d. %s\n", e.Index, e.Format(-1))
		}
		return
	}

	fmt.Println()
	solve(es, maxGain)
}

func solve(es []*statement.Entry, maxGain currency.Value) {
	soln := solver.New(es2e(es)).Solve(maxGain)
	sort.Slice(soln, func(i, j int) bool {
		return statement.EntryLess(soln[i].ID.(*statement.Entry), soln[j].ID.(*statement.Entry))
	})

	var soldValue, soldGains, costBasis currency.Value
	var soldShares int
	for _, elt := range soln {
		e := elt.ID.(*statement.Entry)
		soldShares += elt.N
		vn := currency.Value(elt.N)
		costBasis += vn * e.IssuePrice
		soldValue += vn * elt.Value
		soldGains += vn * elt.Gain
		fmt.Printf("Sell [lot %2d]: %s\n", e.Index, e.Format(elt.N))
	}
	fmt.Printf("\nSold shares:\t%d\nSold value:\t%s\nSold gains:\t%s\nCost basis:\t%s\n",
		soldShares, soldValue.USD(), soldGains.USD(), costBasis.USD())
	if *taxRate > 0 {
		tax := (soldGains * currency.Value(*taxRate)) / 100
		fmt.Printf("%d%% gains tax:\t%s\n", *taxRate, tax.USD())
	}

	// N.B.: We sum the cost bases per lot instead of taking the ending bounds,
	// so that rounding does not occur per transaction.
}

// es2e converts statement entries to solver entries.
func es2e(es []*statement.Entry) []solver.Entry {
	out := make([]solver.Entry, len(es))
	for i, e := range es {
		out[i] = solver.Entry{
			ID:    e,
			N:     e.Available,
			Value: e.Price,
			Gain:  e.Gain,
		}
	}
	return out
}
