package parse_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/pkg/config/internal/parse"
)

// helper: env 키 설정 후 t.Cleanup 으로 자동 unset.
func setEnv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}

func TestPort(t *testing.T) {
	t.Run("empty preserves default", func(t *testing.T) {
		dest := 5432
		err := parse.Port("PARSE_TEST_PORT_EMPTY", &dest)
		assert.NoError(t, err)
		assert.Equal(t, 5432, dest)
	})

	t.Run("valid port", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_PORT_OK", "8080")
		dest := 5432
		err := parse.Port("PARSE_TEST_PORT_OK", &dest)
		assert.NoError(t, err)
		assert.Equal(t, 8080, dest)
	})

	t.Run("zero rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_PORT_ZERO", "0")
		dest := 5432
		err := parse.Port("PARSE_TEST_PORT_ZERO", &dest)
		assert.Error(t, err)
		assert.Equal(t, 5432, dest) // 변경되지 않음
	})

	t.Run("above 65535 rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_PORT_BIG", "70000")
		dest := 5432
		err := parse.Port("PARSE_TEST_PORT_BIG", &dest)
		assert.Error(t, err)
	})

	t.Run("non-numeric rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_PORT_BAD", "abc")
		dest := 5432
		err := parse.Port("PARSE_TEST_PORT_BAD", &dest)
		assert.Error(t, err)
	})
}

func TestPositiveDuration(t *testing.T) {
	t.Run("positive accepted", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_DUR_OK", "5s")
		dest := time.Second
		err := parse.PositiveDuration("PARSE_TEST_DUR_OK", &dest)
		assert.NoError(t, err)
		assert.Equal(t, 5*time.Second, dest)
	})

	t.Run("zero rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_DUR_ZERO", "0s")
		dest := time.Second
		err := parse.PositiveDuration("PARSE_TEST_DUR_ZERO", &dest)
		assert.Error(t, err)
	})

	t.Run("negative rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_DUR_NEG", "-1s")
		dest := time.Second
		err := parse.PositiveDuration("PARSE_TEST_DUR_NEG", &dest)
		assert.Error(t, err)
	})

	t.Run("invalid format rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_DUR_BAD", "abc")
		dest := time.Second
		err := parse.PositiveDuration("PARSE_TEST_DUR_BAD", &dest)
		assert.Error(t, err)
	})
}

func TestNonNegativeDuration(t *testing.T) {
	t.Run("zero accepted", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_NND_ZERO", "0s")
		dest := time.Second
		err := parse.NonNegativeDuration("PARSE_TEST_NND_ZERO", &dest)
		assert.NoError(t, err)
		assert.Equal(t, time.Duration(0), dest)
	})

	t.Run("negative rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_NND_NEG", "-1s")
		dest := time.Second
		err := parse.NonNegativeDuration("PARSE_TEST_NND_NEG", &dest)
		assert.Error(t, err)
	})
}

func TestPositiveInt(t *testing.T) {
	t.Run("positive accepted", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_PI_OK", "10")
		dest := 1
		err := parse.PositiveInt("PARSE_TEST_PI_OK", &dest)
		assert.NoError(t, err)
		assert.Equal(t, 10, dest)
	})

	t.Run("zero rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_PI_ZERO", "0")
		dest := 1
		err := parse.PositiveInt("PARSE_TEST_PI_ZERO", &dest)
		assert.Error(t, err)
	})

	t.Run("negative rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_PI_NEG", "-1")
		dest := 1
		err := parse.PositiveInt("PARSE_TEST_PI_NEG", &dest)
		assert.Error(t, err)
	})
}

func TestNonNegativeInt(t *testing.T) {
	t.Run("zero accepted", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_NNI_ZERO", "0")
		dest := 1
		err := parse.NonNegativeInt("PARSE_TEST_NNI_ZERO", &dest)
		assert.NoError(t, err)
		assert.Equal(t, 0, dest)
	})

	t.Run("negative rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_NNI_NEG", "-1")
		dest := 1
		err := parse.NonNegativeInt("PARSE_TEST_NNI_NEG", &dest)
		assert.Error(t, err)
	})
}

func TestPositiveInt32(t *testing.T) {
	t.Run("positive accepted", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_PI32_OK", "25")
		var dest int32 = 1
		err := parse.PositiveInt32("PARSE_TEST_PI32_OK", &dest)
		assert.NoError(t, err)
		assert.Equal(t, int32(25), dest)
	})

	t.Run("zero rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_PI32_ZERO", "0")
		var dest int32 = 1
		err := parse.PositiveInt32("PARSE_TEST_PI32_ZERO", &dest)
		assert.Error(t, err)
	})
}

func TestNonNegativeInt32(t *testing.T) {
	t.Run("zero accepted", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_NNI32_ZERO", "0")
		var dest int32 = 5
		err := parse.NonNegativeInt32("PARSE_TEST_NNI32_ZERO", &dest)
		assert.NoError(t, err)
		assert.Equal(t, int32(0), dest)
	})

	t.Run("negative rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_NNI32_NEG", "-1")
		var dest int32 = 5
		err := parse.NonNegativeInt32("PARSE_TEST_NNI32_NEG", &dest)
		assert.Error(t, err)
	})
}

func TestNonNegativeInt64(t *testing.T) {
	t.Run("zero accepted (throttle disabled)", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_NNI64_ZERO", "0")
		var dest int64 = 100
		err := parse.NonNegativeInt64("PARSE_TEST_NNI64_ZERO", &dest)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), dest)
	})

	t.Run("negative rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_NNI64_NEG", "-1")
		var dest int64 = 100
		err := parse.NonNegativeInt64("PARSE_TEST_NNI64_NEG", &dest)
		assert.Error(t, err)
	})
}

func TestRatio(t *testing.T) {
	t.Run("in range accepted", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_RATIO_OK", "0.5")
		dest := 0.1
		err := parse.Ratio("PARSE_TEST_RATIO_OK", &dest)
		assert.NoError(t, err)
		assert.InDelta(t, 0.5, dest, 1e-9)
	})

	t.Run("zero accepted", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_RATIO_ZERO", "0")
		dest := 0.5
		err := parse.Ratio("PARSE_TEST_RATIO_ZERO", &dest)
		assert.NoError(t, err)
	})

	t.Run("one accepted", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_RATIO_ONE", "1.0")
		dest := 0.5
		err := parse.Ratio("PARSE_TEST_RATIO_ONE", &dest)
		assert.NoError(t, err)
	})

	t.Run("below zero rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_RATIO_NEG", "-0.1")
		dest := 0.5
		err := parse.Ratio("PARSE_TEST_RATIO_NEG", &dest)
		assert.Error(t, err)
	})

	t.Run("above one rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_RATIO_BIG", "1.1")
		dest := 0.5
		err := parse.Ratio("PARSE_TEST_RATIO_BIG", &dest)
		assert.Error(t, err)
	})
}

func TestBool(t *testing.T) {
	t.Run("true", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_BOOL_T", "true")
		dest := false
		err := parse.Bool("PARSE_TEST_BOOL_T", &dest)
		assert.NoError(t, err)
		assert.True(t, dest)
	})

	t.Run("invalid rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_BOOL_BAD", "yeppers")
		dest := false
		err := parse.Bool("PARSE_TEST_BOOL_BAD", &dest)
		assert.Error(t, err)
	})

	t.Run("empty preserves default", func(t *testing.T) {
		dest := true
		err := parse.Bool("PARSE_TEST_BOOL_EMPTY", &dest)
		assert.NoError(t, err)
		assert.True(t, dest)
	})
}

func TestString(t *testing.T) {
	t.Run("non-empty applies", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_STR_OK", "hello")
		dest := "default"
		err := parse.String("PARSE_TEST_STR_OK", &dest)
		assert.NoError(t, err)
		assert.Equal(t, "hello", dest)
	})

	t.Run("empty preserves default", func(t *testing.T) {
		dest := "default"
		err := parse.String("PARSE_TEST_STR_EMPTY", &dest)
		assert.NoError(t, err)
		assert.Equal(t, "default", dest)
	})
}

func TestEnum(t *testing.T) {
	allowed := []string{"gemini", "openai", "anthropic"}

	t.Run("matched", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_ENUM_OK", "openai")
		dest := "gemini"
		err := parse.Enum("PARSE_TEST_ENUM_OK", allowed, &dest)
		assert.NoError(t, err)
		assert.Equal(t, "openai", dest)
	})

	t.Run("unmatched rejected", func(t *testing.T) {
		setEnv(t, "PARSE_TEST_ENUM_BAD", "qwen")
		dest := "gemini"
		err := parse.Enum("PARSE_TEST_ENUM_BAD", allowed, &dest)
		assert.Error(t, err)
		assert.Equal(t, "gemini", dest)
	})

	t.Run("empty preserves default", func(t *testing.T) {
		dest := "gemini"
		err := parse.Enum("PARSE_TEST_ENUM_EMPTY", allowed, &dest)
		assert.NoError(t, err)
		assert.Equal(t, "gemini", dest)
	})
}
