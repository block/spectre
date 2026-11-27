// The StatsD part of the spectre script API. It augments the shared Egress interface.
declare module "spectre" {
  /** A StatsD metric type. Timers, histograms and distributions are compared alike. */
  export type MetricType = "counter" | "gauge" | "timer" | "histogram" | "distribution" | "set";
  /** A series as one side sent it, or as an earlier rule mapped it. A tag without a value has the value "". */
  export interface Metric {
    readonly name: string;
    readonly type: MetricType;
    readonly tags: Readonly<Record<string, string>>;
    readonly side: "reference" | "candidate";
  }
  /** The series a metric becomes. Leaving out tags keeps the metric's own; scale multiplies its values. */
  export interface MappedMetric {
    name: string;
    tags?: Record<string, string>;
    scale?: number;
  }
  /** Selects by a tag's value, or by a function of it, which receives undefined when the tag is absent. */
  type TagSelect = string | ((value: string | undefined) => boolean);
  /** Allows the candidate to differ from the reference by max(absolute, relative × reference). */
  export interface Tolerance {
    relative?: number;
    absolute?: number;
    /** Fails only when the candidate is higher, or only when it is lower. */
    only?: "higher" | "lower";
  }
  /** Compares a window's counter total, gauge mean, set size, or distribution sample count, and the percentiles of a distribution. */
  export interface MetricComparison extends Tolerance {
    p50?: Tolerance;
    p75?: Tolerance;
    p90?: Tolerance;
    p95?: Tolerance;
    p99?: Tolerance;
  }
  /** Selects metrics of both sides. Rules apply in declaration order, and a series that no rule compares or ignores quarantines. */
  export interface EgressStatsDRule {
    /** Narrows the rule to metrics whose tag matches. */
    tag(name: string, select: TagSelect): EgressStatsDRule;
    /** Maps the selected metrics, then applies the later rules to the result; returning undefined ignores a metric. */
    map(mapping: (metric: Metric) => MappedMetric | undefined): void;
    /** Expects both sides to send the selected series, and compares them in each window. */
    compare(comparison: MetricComparison): void;
    /** Ignores the selected metrics, so they are neither compared nor expected. */
    ignore(): void;
  }
  export interface Egress {
    /** Starts a rule for metrics named like the pattern: "*" matches within a dot-separated segment and "**" across segments. */
    statsd(pattern: string): EgressStatsDRule;
  }
}
