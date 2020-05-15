	"github.com/m3db/m3x/ident"
	xtime "github.com/m3db/m3x/time"
// BlockConfig represents the configuration to generate a SeriesBlock
// Series represents a generated series of data
	Data []ts.Datapoint
// SeriesDataPoint represents a single data point of a generated series of data
	ts.Datapoint
	ID ident.ID
// SeriesDataPointsByTime are a sorted list of SeriesDataPoints
// SeriesBlock is a collection of Series'
// SeriesBlocksByStart is a map of time -> SeriesBlock
// Writer writes generated data to disk
		ns ident.ID, shards sharding.ShardSet, data SeriesBlocksByStart) error
		ns ident.ID,
		ns ident.ID,
		ns ident.ID,
// Options represent the parameters needed for the Writer
	// SetClockOptions sets the clock options
	// ClockOptions returns the clock options
	// SetRetentionPeriod sets how long we intend to keep data in memory
	// RetentionPeriod returns how long we intend to keep data in memory
	// SetBlockSize sets the blockSize
	// BlockSize returns the blockSize
	// SetFilePathPrefix sets the file path prefix for sharded TSDB files
	// FilePathPrefix returns the file path prefix for sharded TSDB files
	// SetNewFileMode sets the new file mode
	// NewFileMode returns the new file mode
	// SetNewDirectoryMode sets the new directory mode
	// NewDirectoryMode returns the new directory mode
	// SetWriterBufferSize sets the buffer size for writing TSDB files
	// WriterBufferSize returns the buffer size for writing TSDB files
	// SetWriteEmptyShards sets whether writes are done even for empty start periods
	// WriteEmptyShards returns whether writes are done even for empty start periods
	// SetWriteSnapshot sets whether writes are written as snapshot files
	// WriteSnapshots returns whether writes are written as snapshot files
	// SetEncoderPool sets the contextPool
	// EncoderPool returns the contextPool
type WriteDatapointPredicate func(dp ts.Datapoint) bool