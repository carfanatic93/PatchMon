import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarClock, Edit, PlayCircle, Plus, Trash2, X } from "lucide-react";
import { useEffect, useState } from "react";
import { useConfirm } from "../../contexts/ConfirmContext";
import { useSettings } from "../../contexts/SettingsContext";
import { useToast } from "../../contexts/ToastContext";
import { adminHostsAPI, formatDate, hostGroupsAPI } from "../../utils/api";
import { patchingAPI } from "../../utils/patchingApi";

const WEEKDAYS = [
	{ value: "1", label: "Monday" },
	{ value: "2", label: "Tuesday" },
	{ value: "3", label: "Wednesday" },
	{ value: "4", label: "Thursday" },
	{ value: "5", label: "Friday" },
	{ value: "6", label: "Saturday" },
	{ value: "0", label: "Sunday" },
];

const FREQUENCIES = [
	{ value: "weekly", label: "Weekly" },
	{ value: "daily", label: "Daily" },
	{ value: "monthly", label: "Monthly" },
	{ value: "custom", label: "Custom (cron)" },
];

const INPUT_CLASS =
	"w-full rounded border border-secondary-300 dark:border-secondary-600 bg-white dark:bg-secondary-700 text-secondary-900 dark:text-white px-3 py-2";
const LABEL_CLASS =
	"block text-sm font-medium text-secondary-700 dark:text-secondary-300 mb-1";

const pad = (n) => String(n).padStart(2, "0");

/** Build a 5-field cron expression from the simple frequency form. */
function buildCron({ frequency, time, weekday, monthDay, cron }) {
	if (frequency === "custom") return cron.trim();
	const [h = "0", m = "0"] = (time || "12:00").split(":");
	const hour = Number(h);
	const minute = Number(m);
	if (frequency === "daily") return `${minute} ${hour} * * *`;
	if (frequency === "monthly") return `${minute} ${hour} ${monthDay} * *`;
	return `${minute} ${hour} * * ${weekday}`;
}

/** Recognise the cron shapes the simple form produces; anything else is "custom". */
function parseCron(expr) {
	const parts = (expr || "").trim().split(/\s+/);
	if (parts.length === 5) {
		const [m, h, dom, mon, dow] = parts;
		const isNum = (v) => /^\d+$/.test(v);
		if (isNum(m) && isNum(h) && mon === "*") {
			const time = `${pad(h)}:${pad(m)}`;
			if (dom === "*" && dow === "*") return { frequency: "daily", time };
			if (dom === "*" && /^[0-6]$/.test(dow))
				return { frequency: "weekly", time, weekday: dow };
			if (isNum(dom) && dow === "*")
				return { frequency: "monthly", time, monthDay: dom };
		}
	}
	return { frequency: "custom", cron: expr || "" };
}

const emptyForm = (tz) => ({
	name: "",
	description: "",
	enabled: true,
	frequency: "weekly",
	time: "12:00",
	weekday: "0",
	monthDay: "1",
	cron: "0 12 * * 0",
	timezone: tz,
	target_type: "all",
	target_ids: [],
});

function describeTarget(schedule, groupsById, hostsById) {
	if (schedule.target_type === "all") return "All hosts";
	const names = (schedule.target_ids || []).map(
		(id) =>
			(schedule.target_type === "host_group"
				? groupsById[id]?.name
				: hostsById[id]) || "unknown",
	);
	const label = schedule.target_type === "host_group" ? "Groups" : "Hosts";
	const shown = names.slice(0, 3).join(", ");
	return `${label}: ${shown}${names.length > 3 ? ` +${names.length - 3}` : ""}`;
}

function describeResult(result) {
	if (!result) return "Never run";
	if (result.status === "skipped_missed") return "Skipped (server was down)";
	if (result.status === "error") return `Error: ${result.error_message || ""}`;
	const parts = [`${result.hosts_queued} queued`];
	if (result.hosts_up_to_date)
		parts.push(`${result.hosts_up_to_date} up to date`);
	if (result.hosts_busy) parts.push(`${result.hosts_busy} busy`);
	if (result.hosts_failed) parts.push(`${result.hosts_failed} failed`);
	return parts.join(", ");
}

export default function SchedulesTab() {
	const queryClient = useQueryClient();
	const toast = useToast();
	const confirm = useConfirm();
	const { settings } = useSettings();
	const orgTimezone = settings?.timezone || "UTC";
	const [showModal, setShowModal] = useState(false);
	const [editing, setEditing] = useState(null);
	const [form, setForm] = useState(emptyForm(orgTimezone));

	const { data: schedules = [], isLoading } = useQuery({
		queryKey: ["patching-schedules"],
		queryFn: () => patchingAPI.getSchedules(),
	});
	const { data: hostGroups = [] } = useQuery({
		queryKey: ["hostGroups"],
		queryFn: () => hostGroupsAPI.list().then((res) => res.data),
	});
	const { data: hostsData } = useQuery({
		queryKey: ["hosts-list", "all"],
		queryFn: () => adminHostsAPI.list({ all: true }).then((res) => res.data),
	});
	const hosts = hostsData?.data || [];
	const groupsById = Object.fromEntries(hostGroups.map((g) => [g.id, g]));
	const hostsById = Object.fromEntries(
		hosts.map((h) => [h.id, h.friendly_name || h.hostname || h.id]),
	);

	const cronExpr = buildCron(form);
	const [previewCron, setPreviewCron] = useState(cronExpr);
	// Debounce so typing a custom cron doesn't fire a request per keystroke.
	useEffect(() => {
		const t = setTimeout(() => setPreviewCron(cronExpr), 300);
		return () => clearTimeout(t);
	}, [cronExpr]);
	const { data: preview, error: previewError } = useQuery({
		queryKey: ["patching-schedule-preview", previewCron, form.timezone],
		queryFn: () => patchingAPI.previewSchedule(previewCron, form.timezone),
		enabled: showModal && !!previewCron,
		retry: false,
	});

	const onError = (err) =>
		toast.error(err.response?.data?.error || err.message);
	const invalidate = () =>
		queryClient.invalidateQueries({ queryKey: ["patching-schedules"] });

	const saveMutation = useMutation({
		mutationFn: ({ id, data }) =>
			id
				? patchingAPI.updateSchedule(id, data)
				: patchingAPI.createSchedule(data),
		onSuccess: () => {
			invalidate();
			setShowModal(false);
			setEditing(null);
			toast.success(editing ? "Schedule updated" : "Schedule created");
		},
		onError,
	});
	const deleteMutation = useMutation({
		mutationFn: (id) => patchingAPI.deleteSchedule(id),
		onSuccess: () => {
			invalidate();
			toast.success("Schedule deleted");
		},
		onError,
	});
	const toggleMutation = useMutation({
		mutationFn: (s) =>
			patchingAPI.updateSchedule(s.id, {
				name: s.name,
				description: s.description,
				cron_expr: s.cron_expr,
				timezone: s.timezone,
				target_type: s.target_type,
				target_ids: s.target_ids,
				enabled: !s.enabled,
			}),
		onSuccess: invalidate,
		onError,
	});
	const runNowMutation = useMutation({
		mutationFn: (id) => patchingAPI.runScheduleNow(id),
		onSuccess: (res) => {
			invalidate();
			queryClient.invalidateQueries({ queryKey: ["patching-runs"] });
			toast.success(`Run started: ${describeResult(res)}`);
		},
		onError,
	});

	const openCreate = () => {
		setEditing(null);
		setForm(emptyForm(orgTimezone));
		setShowModal(true);
	};
	const openEdit = (s) => {
		setEditing(s);
		setForm({
			...emptyForm(s.timezone),
			...parseCron(s.cron_expr),
			cron: s.cron_expr,
			name: s.name,
			description: s.description || "",
			enabled: s.enabled,
			timezone: s.timezone,
			target_type: s.target_type,
			target_ids: s.target_ids || [],
		});
		setShowModal(true);
	};
	const toggleTarget = (id) =>
		setForm((f) => ({
			...f,
			target_ids: f.target_ids.includes(id)
				? f.target_ids.filter((x) => x !== id)
				: [...f.target_ids, id],
		}));

	const handleSubmit = (e) => {
		e.preventDefault();
		saveMutation.mutate({
			id: editing?.id,
			data: {
				name: form.name.trim(),
				description: form.description.trim(),
				cron_expr: cronExpr,
				timezone: form.timezone,
				enabled: form.enabled,
				target_type: form.target_type,
				target_ids: form.target_type === "all" ? [] : form.target_ids,
			},
		});
	};

	const targetOptions =
		form.target_type === "host_group"
			? hostGroups.map((g) => ({ id: g.id, name: g.name }))
			: hosts.map((h) => ({
					id: h.id,
					name: h.friendly_name || h.hostname || h.id,
				}));

	return (
		<div className="mt-4 space-y-4">
			<div className="flex items-center justify-between gap-4">
				<p className="text-sm text-secondary-600 dark:text-secondary-400">
					Schedules install all pending updates (patch all) automatically on a
					recurring basis. Only hosts with pending updates are patched. Hosts
					that already have a run in progress are skipped.
				</p>
				<button
					type="button"
					onClick={openCreate}
					className="btn-primary flex items-center gap-2 shrink-0"
				>
					<Plus className="h-4 w-4" />
					Create schedule
				</button>
			</div>

			<div className="card p-4 md:p-6">
				{isLoading ? (
					<div className="p-8 text-center text-secondary-500">Loading...</div>
				) : schedules.length === 0 ? (
					<div className="p-8 text-center text-secondary-500">
						No schedules yet. Create one, e.g. every Sunday at 12:00.
					</div>
				) : (
					<div className="overflow-x-auto">
						<table className="min-w-full text-sm">
							<thead>
								<tr className="text-left text-secondary-500 dark:text-secondary-400 border-b border-secondary-200 dark:border-secondary-600">
									<th className="py-2 pr-4">Name</th>
									<th className="py-2 pr-4">Schedule</th>
									<th className="py-2 pr-4">Targets</th>
									<th className="py-2 pr-4">Next run</th>
									<th className="py-2 pr-4">Last run</th>
									<th className="py-2 pr-4">Enabled</th>
									<th className="py-2 text-right">Actions</th>
								</tr>
							</thead>
							<tbody>
								{schedules.map((s) => (
									<tr
										key={s.id}
										className="border-b border-secondary-100 dark:border-secondary-700 text-secondary-900 dark:text-white"
									>
										<td className="py-3 pr-4 font-medium">
											{s.name}
											{s.description && (
												<div className="text-xs font-normal text-secondary-500">
													{s.description}
												</div>
											)}
										</td>
										<td className="py-3 pr-4">
											<code className="text-xs">{s.cron_expr}</code>
											<div className="text-xs text-secondary-500">
												{s.timezone}
											</div>
										</td>
										<td className="py-3 pr-4">
											{describeTarget(s, groupsById, hostsById)}
										</td>
										<td className="py-3 pr-4">
											{s.enabled && s.next_run_at
												? formatDate(s.next_run_at, s.timezone)
												: " -"}
										</td>
										<td className="py-3 pr-4">
											{s.last_run_at
												? formatDate(s.last_run_at, s.timezone)
												: ""}
											<div className="text-xs text-secondary-500">
												{describeResult(s.last_run_result)}
											</div>
										</td>
										<td className="py-3 pr-4">
											<input
												type="checkbox"
												checked={s.enabled}
												onChange={() => toggleMutation.mutate(s)}
												aria-label={`Enable ${s.name}`}
											/>
										</td>
										<td className="py-3 text-right whitespace-nowrap">
											<button
												type="button"
												title="Run now"
												disabled={runNowMutation.isPending}
												onClick={async () => {
													if (
														await confirm({
															title: "Run schedule now",
															message: `Install pending updates now on all hosts targeted by "${s.name}"?`,
															confirmLabel: "Run now",
														})
													)
														runNowMutation.mutate(s.id);
												}}
												className="p-1 rounded hover:bg-secondary-100 dark:hover:bg-secondary-700"
											>
												<PlayCircle className="h-4 w-4" />
											</button>
											<button
												type="button"
												title="Edit"
												onClick={() => openEdit(s)}
												className="p-1 rounded hover:bg-secondary-100 dark:hover:bg-secondary-700"
											>
												<Edit className="h-4 w-4" />
											</button>
											<button
												type="button"
												title="Delete"
												onClick={async () => {
													if (
														await confirm({
															title: "Delete schedule",
															message: `Delete the schedule "${s.name}"? Runs it already created are kept.`,
															confirmLabel: "Delete schedule",
														})
													)
														deleteMutation.mutate(s.id);
												}}
												className="p-1 rounded hover:bg-secondary-100 dark:hover:bg-secondary-700 text-red-600"
											>
												<Trash2 className="h-4 w-4" />
											</button>
										</td>
									</tr>
								))}
							</tbody>
						</table>
					</div>
				)}
			</div>

			{showModal && (
				<div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50">
					<div className="bg-white dark:bg-secondary-800 rounded-lg shadow-xl max-w-lg w-full max-h-[90vh] overflow-y-auto">
						<div className="flex items-center justify-between p-4 border-b border-secondary-200 dark:border-secondary-600">
							<h3 className="text-lg font-semibold text-secondary-900 dark:text-white flex items-center gap-2">
								<CalendarClock className="h-5 w-5" />
								{editing ? "Edit schedule" : "Create schedule"}
							</h3>
							<button
								type="button"
								onClick={() => setShowModal(false)}
								className="p-1 rounded hover:bg-secondary-100 dark:hover:bg-secondary-700"
							>
								<X className="h-5 w-5" />
							</button>
						</div>
						<form onSubmit={handleSubmit} className="p-4 space-y-4">
							<div>
								<label className={LABEL_CLASS} htmlFor="sched-name">
									Name
								</label>
								<input
									id="sched-name"
									type="text"
									value={form.name}
									onChange={(e) =>
										setForm((f) => ({ ...f, name: e.target.value }))
									}
									className={INPUT_CLASS}
									placeholder="Weekly updates"
									required
									maxLength={100}
								/>
							</div>
							<div>
								<label className={LABEL_CLASS} htmlFor="sched-desc">
									Description (optional)
								</label>
								<input
									id="sched-desc"
									type="text"
									value={form.description}
									onChange={(e) =>
										setForm((f) => ({ ...f, description: e.target.value }))
									}
									className={INPUT_CLASS}
								/>
							</div>

							<div className="grid grid-cols-2 gap-3">
								<div>
									<label className={LABEL_CLASS} htmlFor="sched-freq">
										Repeat
									</label>
									<select
										id="sched-freq"
										value={form.frequency}
										onChange={(e) => {
											const frequency = e.target.value;
											setForm((f) => ({
												...f,
												frequency,
												// Carry the current expression into the editor when switching to custom.
												cron: frequency === "custom" ? buildCron(f) : f.cron,
											}));
										}}
										className={INPUT_CLASS}
									>
										{FREQUENCIES.map((o) => (
											<option key={o.value} value={o.value}>
												{o.label}
											</option>
										))}
									</select>
								</div>
								{form.frequency === "weekly" && (
									<div>
										<label className={LABEL_CLASS} htmlFor="sched-weekday">
											Day
										</label>
										<select
											id="sched-weekday"
											value={form.weekday}
											onChange={(e) =>
												setForm((f) => ({ ...f, weekday: e.target.value }))
											}
											className={INPUT_CLASS}
										>
											{WEEKDAYS.map((d) => (
												<option key={d.value} value={d.value}>
													{d.label}
												</option>
											))}
										</select>
									</div>
								)}
								{form.frequency === "monthly" && (
									<div>
										<label className={LABEL_CLASS} htmlFor="sched-monthday">
											Day of month
										</label>
										<select
											id="sched-monthday"
											value={form.monthDay}
											onChange={(e) =>
												setForm((f) => ({ ...f, monthDay: e.target.value }))
											}
											className={INPUT_CLASS}
										>
											{Array.from({ length: 28 }, (_, i) => String(i + 1)).map(
												(d) => (
													<option key={d} value={d}>
														{d}
													</option>
												),
											)}
										</select>
									</div>
								)}
							</div>

							{form.frequency === "custom" ? (
								<div>
									<label className={LABEL_CLASS} htmlFor="sched-cron">
										Cron expression (minute hour day-of-month month day-of-week)
									</label>
									<input
										id="sched-cron"
										type="text"
										value={form.cron}
										onChange={(e) =>
											setForm((f) => ({ ...f, cron: e.target.value }))
										}
										className={`${INPUT_CLASS} font-mono`}
										placeholder="0 12 * * 0"
										required
									/>
								</div>
							) : (
								<div>
									<label className={LABEL_CLASS} htmlFor="sched-time">
										Time
									</label>
									<input
										id="sched-time"
										type="time"
										value={form.time}
										onChange={(e) =>
											setForm((f) => ({ ...f, time: e.target.value }))
										}
										className={INPUT_CLASS}
										required
									/>
								</div>
							)}

							<div>
								<label className={LABEL_CLASS} htmlFor="sched-tz">
									Timezone (IANA)
								</label>
								<input
									id="sched-tz"
									type="text"
									value={form.timezone}
									onChange={(e) =>
										setForm((f) => ({ ...f, timezone: e.target.value }))
									}
									className={INPUT_CLASS}
									placeholder="Europe/Berlin"
									required
								/>
							</div>

							<div className="text-sm rounded bg-secondary-50 dark:bg-secondary-700 p-3">
								{previewError ? (
									<span className="text-red-600">
										{previewError.response?.data?.error || "Invalid schedule"}
									</span>
								) : preview?.next_runs?.length ? (
									<>
										<div className="font-medium text-secondary-700 dark:text-secondary-200 mb-1">
											Next runs
										</div>
										<ul className="text-secondary-600 dark:text-secondary-300">
											{preview.next_runs.slice(0, 3).map((t) => (
												<li key={t}>{formatDate(t, form.timezone)}</li>
											))}
										</ul>
									</>
								) : (
									<span className="text-secondary-500">Calculating...</span>
								)}
							</div>

							<div>
								<label className={LABEL_CLASS} htmlFor="sched-target">
									Target
								</label>
								<select
									id="sched-target"
									value={form.target_type}
									onChange={(e) =>
										setForm((f) => ({
											...f,
											target_type: e.target.value,
											target_ids: [],
										}))
									}
									className={INPUT_CLASS}
								>
									<option value="all">All hosts</option>
									<option value="host_group">Host groups</option>
									<option value="host">Individual hosts</option>
								</select>
								{form.target_type !== "all" && (
									<div className="mt-2 max-h-40 overflow-y-auto rounded border border-secondary-300 dark:border-secondary-600 p-2 space-y-1">
										{targetOptions.length === 0 ? (
											<div className="text-sm text-secondary-500">
												Nothing to select.
											</div>
										) : (
											targetOptions.map((o) => (
												<label
													key={o.id}
													className="flex items-center gap-2 text-sm text-secondary-900 dark:text-white"
												>
													<input
														type="checkbox"
														checked={form.target_ids.includes(o.id)}
														onChange={() => toggleTarget(o.id)}
													/>
													{o.name}
												</label>
											))
										)}
									</div>
								)}
							</div>

							<label className="flex items-center gap-2 text-sm text-secondary-900 dark:text-white">
								<input
									type="checkbox"
									checked={form.enabled}
									onChange={(e) =>
										setForm((f) => ({ ...f, enabled: e.target.checked }))
									}
								/>
								Enabled
							</label>

							<div className="flex justify-end gap-2 pt-2">
								<button
									type="button"
									onClick={() => setShowModal(false)}
									className="btn-outline"
								>
									Cancel
								</button>
								<button
									type="submit"
									className="btn-primary"
									disabled={
										saveMutation.isPending ||
										!!previewError ||
										(form.target_type !== "all" && form.target_ids.length === 0)
									}
								>
									{saveMutation.isPending ? "Saving..." : "Save"}
								</button>
							</div>
						</form>
					</div>
				</div>
			)}
		</div>
	);
}
