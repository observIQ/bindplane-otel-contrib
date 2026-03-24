// AST types for STIX 2.1 pattern expressions.
// Used by the matcher to evaluate patterns against observed-data.

package pattern

// Pattern is the root AST node: observationExpressions (top-level FOLLOWEDBY sequence).
type Pattern struct {
	ObservationExpressions *ObservationExpressions
}

// ObservationExpressions is a sequence of observation expressions joined by FOLLOWEDBY.
type ObservationExpressions struct {
	Ors []*ObservationExpressionOr
}

// ObservationExpressionOr is one or more ObservationExpressionAnd joined by OR.
type ObservationExpressionOr struct {
	Ands []*ObservationExpressionAnd
}

// ObservationExpressionAnd is one or more ObservationExpression joined by AND.
type ObservationExpressionAnd struct {
	Exprs []*ObservationExpression
}

// ObservationExpression is either [ comparisonExpression ] or ( observationExpressions ), with optional qualifiers.
type ObservationExpression struct {
	// One of Comparison or Nested is set.
	Comparison *ComparisonExpression
	Nested     *ObservationExpressions
	// Qualifiers (in order)
	StartStop *StartStopQualifier
	Within    *WithinQualifier
	Repeats   *RepeatsQualifier
}

// StartStopQualifier is START t STOP t.
type StartStopQualifier struct {
	Start, Stop Literal // timestamp literals
}

// WithinQualifier is WITHIN n SECONDS.
type WithinQualifier struct {
	Seconds Literal // float or int
}

// RepeatsQualifier is REPEATS n TIMES.
type RepeatsQualifier struct {
	Times int
}

// ComparisonExpression is comparisonExpressionOr OR comparisonExpressionOr OR ...
type ComparisonExpression struct {
	Ands []*ComparisonExpressionAnd
}

// ComparisonExpressionAnd is comparisonExpressionAnd AND comparisonExpressionAnd AND ...
type ComparisonExpressionAnd struct {
	PropTests []PropTest
}

// PropTest is a single property test (equality, order, set, like, regex, exists, or nested comparison).
type PropTest interface {
	propTest()
}

// PropTestEqual is objectPath NOT? (EQ|NEQ) primitiveLiteral.
type PropTestEqual struct {
	ObjectPath *ObjectPath
	Not        bool
	Op         string // "=" or "!="
	Literal    Literal
}

func (*PropTestEqual) propTest() {}

// PropTestOrder is objectPath NOT? (GT|LT|GE|LE) orderableLiteral.
type PropTestOrder struct {
	ObjectPath *ObjectPath
	Not        bool
	Op         string // ">", "<", ">=", "<="
	Literal    Literal
}

func (*PropTestOrder) propTest() {}

// PropTestSet is objectPath NOT? IN setLiteral.
type PropTestSet struct {
	ObjectPath *ObjectPath
	Not        bool
	Set        *SetLiteral
}

func (*PropTestSet) propTest() {}

// PropTestLike is objectPath NOT? LIKE stringLiteral.
type PropTestLike struct {
	ObjectPath *ObjectPath
	Not        bool
	Pattern    string
}

func (*PropTestLike) propTest() {}

// PropTestRegex is objectPath NOT? MATCHES stringLiteral.
type PropTestRegex struct {
	ObjectPath *ObjectPath
	Not        bool
	Regex      string
}

func (*PropTestRegex) propTest() {}

// PropTestIsSubset is objectPath NOT? ISSUBSET stringLiteral.
type PropTestIsSubset struct {
	ObjectPath *ObjectPath
	Not        bool
	Value      string
}

func (*PropTestIsSubset) propTest() {}

// PropTestIsSuperset is objectPath NOT? ISSUPERSET stringLiteral.
type PropTestIsSuperset struct {
	ObjectPath *ObjectPath
	Not        bool
	Value      string
}

func (*PropTestIsSuperset) propTest() {}

// PropTestExists is NOT? EXISTS objectPath.
type PropTestExists struct {
	Not        bool
	ObjectPath *ObjectPath
}

func (*PropTestExists) propTest() {}

// PropTestParens is ( comparisonExpression ).
type PropTestParens struct {
	Comparison *ComparisonExpression
}

func (*PropTestParens) propTest() {}

// ObjectPath is objectType : firstPathComponent pathComponent*.
type ObjectPath struct {
	ObjectType string
	Path       []PathStep
}

// PathStep is one step in a path: .key, [index], or [*].
type PathStep struct {
	Key   string // for .key (identifier or string literal)
	Index *int   // for [n]; nil for [*]
	Star  bool   // true for [*]
}

// SetLiteral is ( literal ( , literal )* ).
type SetLiteral struct {
	Values []Literal
}

// Literal represents a primitive or orderable literal (int, float, string, bool, binary, hex, timestamp).
type Literal struct {
	Int       *int64
	Float     *float64
	Str       *string
	Bool      *bool
	Binary    []byte
	Hex       []byte
	Timestamp *string
}
