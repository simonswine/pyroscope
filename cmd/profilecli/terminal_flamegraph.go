package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/google/pprof/profile"
	"github.com/rivo/tview"
)

// Node represents a node in the flamegraph
type FlameNode struct {
	Name      string
	Value     int64
	Children  map[string]*FlameNode
	Parent    *FlameNode
	Collapsed bool
}

type flamegraphParams struct {
	*queryProfileParams
	filePath string
}

func addFlamegraphParams(queryCmd commander) *flamegraphParams {
	params := new(flamegraphParams)
	params.queryProfileParams = addQueryProfileParams(queryCmd)
	queryCmd.Arg("file", "Path to the profile file").Required().StringVar(&params.filePath)
	return params
}

func showTerminalFlamegraph(ctx context.Context, params *flamegraphParams) error {
	// Parse the profile
	f, err := os.Open(params.filePath)
	if err != nil {
		return fmt.Errorf("failed to open temporary file: %w", err)
	}
	defer f.Close()

	prof, err := profile.Parse(f)
	if err != nil {
		return fmt.Errorf("failed to parse profile: %w", err)
	}

	// Build the flamegraph tree
	root := buildFlameGraph(prof)

	// Create the terminal UI
	return showFlameGraphUI(root)
}

// buildFlameGraph builds a flamegraph tree from a profile
func buildFlameGraph(prof *profile.Profile) *FlameNode {
	root := &FlameNode{
		Name:     "root",
		Value:    0,
		Children: make(map[string]*FlameNode),
	}

	// Process each sample in the profile
	for _, sample := range prof.Sample {
		value := int64(sample.Value[0]) // Use the first value (usually CPU time)
		root.Value += value

		// Start from the root for each stack trace
		current := root

		// Process the stack trace from bottom to top (leaf to root)
		// This is the typical way to build a flamegraph
		for i := len(sample.Location) - 1; i >= 0; i-- {
			loc := sample.Location[i]

			// Get function name from the location
			var funcName string
			if len(loc.Line) > 0 && loc.Line[0].Function != nil {
				funcName = loc.Line[0].Function.Name
			} else {
				funcName = fmt.Sprintf("0x%x", loc.Address)
			}

			// Create or update the child node
			child, exists := current.Children[funcName]
			if !exists {
				child = &FlameNode{
					Name:     funcName,
					Value:    0,
					Children: make(map[string]*FlameNode),
					Parent:   current,
				}
				current.Children[funcName] = child
			}

			child.Value += value
			current = child
		}
	}

	return root
}

// showFlameGraphUI displays the flamegraph in a terminal UI
func showFlameGraphUI(root *FlameNode) error {
	app := tview.NewApplication()

	// Create a text view for the flamegraph visualization
	flamegraph := tview.NewTextView()
	flamegraph.SetDynamicColors(true)
	flamegraph.SetRegions(true)
	flamegraph.SetWordWrap(false)
	flamegraph.SetTitle("Flamegraph")
	flamegraph.SetBorder(true)

	// Create a text view for details
	details := tview.NewTextView()
	details.SetDynamicColors(true)
	details.SetRegions(true)
	details.SetWordWrap(true)
	details.SetTitle("Details")
	details.SetBorder(true)

	// Create a help text view
	help := tview.NewTextView()
	help.SetDynamicColors(true)
	help.SetText("↑/↓: Navigate Levels  ←/→: Navigate Functions  q: Quit  h: Toggle Help")
	help.SetTextAlign(tview.AlignCenter)
	help.SetBorder(true)

	// Create a flex layout
	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(flamegraph, 0, 1, true).
		AddItem(details, 10, 1, false).
		AddItem(help, 1, 1, false)

	// Set up navigation state
	currentLevel := 0
	maxLevel := getMaxDepth(root)
	selectedNode := root
	selectedColumn := 0

	// Render the flamegraph
	renderFlamegraph(flamegraph, root, currentLevel, selectedColumn)

	updateDetails := func(node *FlameNode) {
		detailsText := fmt.Sprintf("Function: %s\n", node.Name)
		detailsText += fmt.Sprintf("Value: %d\n", node.Value)

		if node.Parent != nil {
			percentage := float64(node.Value) / float64(node.Parent.Value) * 100
			detailsText += fmt.Sprintf("Percentage of parent: %.2f%%\n", percentage)
		}

		if root.Value > 0 {
			percentage := float64(node.Value) / float64(root.Value) * 100
			detailsText += fmt.Sprintf("Percentage of total: %.2f%%\n", percentage)
		}

		childCount := len(node.Children)
		detailsText += fmt.Sprintf("Children: %d\n", childCount)

		details.SetText(detailsText)
	}

	updateDetails(selectedNode)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyRune:
			switch event.Rune() {
			case 'q':
				app.Stop()
				return nil
			case 'h':
				// Toggle help visibility
				if flex.GetItemCount() == 3 {
					flex.RemoveItem(help)
				} else {
					flex.AddItem(help, 1, 1, false)
				}
				return nil
			}
		case tcell.KeyUp:
			if currentLevel > 0 {
				currentLevel--
				selectedColumn = 0 // Reset column selection when changing levels
				selectedNode = getNodeAtLevel(root, currentLevel)
				updateDetails(selectedNode)
				renderFlamegraph(flamegraph, root, currentLevel, selectedColumn)
			}
			return nil
		case tcell.KeyDown:
			if currentLevel < maxLevel {
				currentLevel++
				selectedColumn = 0 // Reset column selection when changing levels
				selectedNode = getNodeAtLevel(root, currentLevel)
				updateDetails(selectedNode)
				renderFlamegraph(flamegraph, root, currentLevel, selectedColumn)
			}
			return nil
		case tcell.KeyLeft:
			// Navigate to previous function in the current level
			if selectedColumn > 0 {
				selectedColumn--
				selectedNode = getNodeAtPosition(root, currentLevel, selectedColumn)
				updateDetails(selectedNode)
				renderFlamegraph(flamegraph, root, currentLevel, selectedColumn)
			}
			return nil
		case tcell.KeyRight:
			// Navigate to next function in the current level
			maxColumns := countNodesAtLevel(root, currentLevel)
			if selectedColumn < maxColumns-1 {
				selectedColumn++
				selectedNode = getNodeAtPosition(root, currentLevel, selectedColumn)
				updateDetails(selectedNode)
				renderFlamegraph(flamegraph, root, currentLevel, selectedColumn)
			}
			return nil
		}
		return event
	})

	// Run the application
	if err := app.SetRoot(flex, true).EnableMouse(true).Run(); err != nil {
		return err
	}

	return nil
}

// renderFlamegraph renders the flamegraph visualization
func renderFlamegraph(view *tview.TextView, root *FlameNode, highlightLevel int, highlightColumn int) {
	view.Clear()

	// Get terminal width
	_, _, width, _ := view.GetInnerRect()
	if width <= 0 {
		width = 80 // Default width if we can't determine the actual width
	}

	// Render each level of the flamegraph
	maxDepth := getMaxDepth(root)
	for level := 1; level <= maxDepth; level++ {
		isHighlightedLevel := level == highlightLevel
		renderLevel(view, root, level, 0, width, root.Value, isHighlightedLevel, highlightColumn)
		view.Write([]byte("\n"))
	}
}

// renderLevel renders a single level of the flamegraph
func renderLevel(view *tview.TextView, node *FlameNode, targetLevel, currentLevel, width int, totalValue int64, isHighlightedLevel bool, highlightColumn int) {
	if currentLevel == targetLevel {
		// Calculate the width of this node's block
		blockWidth := int(float64(node.Value) / float64(totalValue) * float64(width))
		if blockWidth < 1 {
			blockWidth = 1
		}

		// Choose a color based on the function name (for consistency)
		colorIndex := getColorIndex(node.Name)
		colors := []string{"wheat", "violet", "green", "yellow", "blue", "magenta", "cyan", "white"}
		textColors := []string{"black", "black", "black", "black", "white", "black", "black", "black"}
		color := colors[colorIndex%len(colors)]
		textColor := textColors[colorIndex%len(textColors)]

		// Create a block with the node's name
		name := node.Name
		if len(name) > blockWidth-2 { // Leave space for brackets
			if blockWidth > 5 {
				name = name[:blockWidth-5] + "..."
			} else if blockWidth > 2 {
				name = name[:blockWidth-2]
			} else {
				name = ""
			}
		}

		// Create the block with color
		block := fmt.Sprintf("[%s:%s]", textColor, color)

		// Add the name if there's space
		if len(name) > 0 {
			// Center the name in the block
			leftPadding := (blockWidth - len(name)) / 2
			rightPadding := blockWidth - len(name) - leftPadding

			if leftPadding > 0 {
				block += strings.Repeat(" ", leftPadding)
			}

			block += name

			if rightPadding > 0 {
				block += strings.Repeat(" ", rightPadding)
			}
		} else {
			// Just fill with blocks
			block += strings.Repeat(" ", blockWidth)
		}

		block += "[:-]" // Reset color

		// Write the block
		view.Write([]byte(block))
		return
	}

	if currentLevel < targetLevel {
		// Track the total width used so far
		var usedWidth int = 0
		var columnIndex int = 0

		// Recursively render children
		children := sortNodesByValue(node)
		for _, child := range children {
			// Calculate child's proportion of parent
			childProportion := float64(child.Value) / float64(node.Value)

			// Calculate child's width based on parent's total width
			childWidth := int(childProportion * float64(width))

			// Ensure we don't exceed the total width
			remainingWidth := width - usedWidth
			if childWidth > remainingWidth {
				childWidth = remainingWidth
			}

			if childWidth > 0 {
				// Check if this column should be highlighted
				isHighlighted := isHighlightedLevel && columnIndex == highlightColumn

				if isHighlighted {
					view.Write([]byte("[::ur]"))
				}

				renderLevel(view, child, targetLevel, currentLevel+1, childWidth, child.Value, isHighlightedLevel, highlightColumn-columnIndex)

				if isHighlighted {
					view.Write([]byte("[::UR]"))
				}

				usedWidth += childWidth
				columnIndex++
			}
		}

		// Fill any remaining space (due to rounding)
		if usedWidth < width {
			view.Write([]byte(strings.Repeat(" ", width-usedWidth)))
		}
	}
}

// getColorIndex generates a consistent color index based on a string
func getColorIndex(s string) int {
	var hash int
	for i := 0; i < len(s); i++ {
		hash = 31*hash + int(s[i])
	}
	if hash < 0 {
		hash = -hash
	}
	return hash
}

// getMaxDepth returns the maximum depth of the flamegraph tree
func getMaxDepth(node *FlameNode) int {
	if len(node.Children) == 0 {
		return 0
	}

	maxChildDepth := 0
	for _, child := range node.Children {
		childDepth := getMaxDepth(child)
		if childDepth > maxChildDepth {
			maxChildDepth = childDepth
		}
	}

	return maxChildDepth + 1
}

// getNodeAtLevel returns a representative node at the given level
func getNodeAtLevel(node *FlameNode, level int) *FlameNode {
	if level == 0 {
		return node
	}

	if len(node.Children) == 0 {
		return node
	}

	// Get the child with the highest value
	var highestChild *FlameNode
	highestValue := int64(0)

	for _, child := range node.Children {
		if child.Value > highestValue {
			highestValue = child.Value
			highestChild = child
		}
	}

	if highestChild == nil {
		return node
	}

	return getNodeAtLevel(highestChild, level-1)
}

// sortNodesByValue returns the children of a node sorted by value (descending)
func sortNodesByValue(node *FlameNode) []*FlameNode {
	children := make([]*FlameNode, 0, len(node.Children))
	for _, child := range node.Children {
		children = append(children, child)
	}

	sort.Slice(children, func(i, j int) bool {
		return children[i].Value > children[j].Value
	})

	return children
}

// getNodeAtPosition returns the node at the specified position (column) in the given level
func getNodeAtPosition(root *FlameNode, level, position int) *FlameNode {
	if level == 0 {
		return root
	}

	// Get all nodes at this level
	nodes := getNodesAtLevel(root, level)

	// Return the node at the requested position, or the last node if position is out of bounds
	if position < len(nodes) {
		return nodes[position]
	} else if len(nodes) > 0 {
		return nodes[len(nodes)-1]
	}

	// Fallback to the root if no nodes found
	return root
}

// getNodesAtLevel returns all nodes at the specified level
func getNodesAtLevel(root *FlameNode, level int) []*FlameNode {
	if level == 0 {
		return []*FlameNode{root}
	}

	var result []*FlameNode

	var traverse func(node *FlameNode, currentLevel int)
	traverse = func(node *FlameNode, currentLevel int) {
		if currentLevel == level {
			result = append(result, node)
			return
		}

		if currentLevel < level {
			for _, child := range sortNodesByValue(node) {
				traverse(child, currentLevel+1)
			}
		}
	}

	traverse(root, 0)
	return result
}

// countNodesAtLevel counts the number of nodes at the specified level
func countNodesAtLevel(root *FlameNode, level int) int {
	return len(getNodesAtLevel(root, level))
}
