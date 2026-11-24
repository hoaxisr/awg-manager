// Тумблер «Маршрутизация через sing-box» в режиме устройств «все» ничего не
// решает: tproxy прыгает в свою цепочку из PREROUTING безусловно, MARK по
// входным интерфейсам там не эмитится (internal/singbox/router/iptables.go), и
// трафик абонентов идёт через sing-box при выключенном тумблере тоже. Прежняя
// подсказка обещала обратное — «абоненты выходят напрямую, минуя правила».
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { WdttServerConfig } from "$lib/types";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "matchMedia", {
    writable: true,
    configurable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }),
  });
});

const apiMock = vi.hoisted(() => ({
  listManagedLANSegments: vi.fn().mockResolvedValue([]),
  getWANInterfaces: vi.fn().mockResolvedValue([]),
  singboxRouterGetSettings: vi.fn(),
  singboxGetStatus: vi.fn().mockResolvedValue({ running: true }),
  getWdttServerPanelUsers: vi.fn().mockResolvedValue({ users: [] }),
  rotateWdttServerDiagKey: vi.fn().mockResolvedValue({ rotated: true }),
}));
vi.mock("$lib/api/client", () => ({ api: apiMock }));

vi.mock("$lib/stores/servers", () => ({
  servers: { subscribe: () => () => {} },
}));

const notify = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  info: vi.fn(),
}));
vi.mock("$lib/stores/notifications", () => ({ notifications: notify }));

import { render, fireEvent, waitFor, within } from "@testing-library/svelte";
import ShareDetail from "./ShareDetail.svelte";
import type { ProxyInstanceRow } from "./rows";

const row: ProxyInstanceRow = {
  key: "wdtt:server:default",
  id: "default",
  protocol: "wdtt",
  role: "server",
  name: "Сервер",
  state: "stopped",
  autostart: false,
  orphanedPid: false,
  binaryPresent: true,
  mode: "wg",
};

const wdttServer: WdttServerConfig = {
  listen: "0.0.0.0:56002",
  wgPort: 56001,
  configDir: "/opt/etc/awg-manager/wdtt/server/default",
  natMode: "full",
  relayMode: "wg",
  openFirewall: true,
  wgIface: "opkgtun17",
  rawIface: "opkgtun18",
};

/** Открывает ящик настроек: тумблер живёт там, а не на карточке. */
async function mountAndOpenSettings() {
  const r = render(ShareDetail, {
    props: {
      row,
      wdttServer,
      onstart: () => {},
      onstop: () => {},
      onrestart: async () => {},
      onsave: async () => null,
      onreload: () => {},
    },
  });
  await waitFor(() =>
    expect(apiMock.singboxRouterGetSettings).toHaveBeenCalled(),
  );
  await fireEvent.click(r.getByRole("button", { name: "Настройки" }));
  return r;
}

function settings(over: Record<string, unknown> = {}) {
  return {
    enabled: true,
    policyName: "p1",
    deviceMode: "policy",
    routingMode: "tproxy",
    snifferEnabled: true,
    wanAutoDetect: true,
    ingressInterfaces: [],
    ...over,
  };
}

describe("ShareDetail: подсказка тумблера ingress", () => {
  beforeEach(() => vi.clearAllMocks());

  it("режим устройств «все» в tproxy — честная подсказка вместо обещания прямого выхода", async () => {
    apiMock.singboxRouterGetSettings.mockResolvedValue(
      settings({ deviceMode: "all" }),
    );
    const { findByText, queryByText } = await mountAndOpenSettings();

    await findByText(
      "Режим устройств «все» — трафик абонентов и так идёт через sing-box",
    );
    expect(queryByText(/выходят напрямую/)).toBeNull();
  });

  it("режим «по политике» — подсказки нет, тумблер работает как написано", async () => {
    apiMock.singboxRouterGetSettings.mockResolvedValue(settings());
    const { queryByText } = await mountAndOpenSettings();

    await waitFor(() =>
      expect(queryByText(/Режим устройств «все»/)).toBeNull(),
    );
  });

  it("в policy-tun режим устройств не участвует в захвате — подсказки нет", async () => {
    apiMock.singboxRouterGetSettings.mockResolvedValue(
      settings({ deviceMode: "all", routingMode: "policy-tun" }),
    );
    const { queryByText } = await mountAndOpenSettings();

    await waitFor(() =>
      expect(queryByText(/Режим устройств «все»/)).toBeNull(),
    );
  });

  it("остановленный sing-box перебивает подсказку про режим: тогда не работает ничего", async () => {
    apiMock.singboxRouterGetSettings.mockResolvedValue(
      settings({ deviceMode: "all" }),
    );
    apiMock.singboxGetStatus.mockResolvedValue({ running: false });
    const { findByText, queryByText } = await mountAndOpenSettings();

    await findByText(
      "sing-box не запущен — правило вступит в силу после его запуска",
    );
    expect(queryByText(/Режим устройств «все»/)).toBeNull();
  });
});

describe("ShareDetail: смена ключа диагностики", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiMock.singboxRouterGetSettings.mockResolvedValue(settings());
    apiMock.singboxGetStatus.mockResolvedValue({ running: true });
    apiMock.rotateWdttServerDiagKey.mockResolvedValue({ rotated: true });
  });

  it("кнопка «Сменить ключ» вызывает rotateWdttServerDiagKey и перезапускает запущенный сервер", async () => {
    const onrestart = vi.fn().mockResolvedValue(undefined);
    const r = render(ShareDetail, {
      props: {
        row: { ...row, state: "running" },
        wdttServer,
        onstart: () => {},
        onstop: () => {},
        onrestart,
        onsave: async () => null,
        onreload: () => {},
      },
    });
    await waitFor(() =>
      expect(apiMock.singboxRouterGetSettings).toHaveBeenCalled(),
    );
    await fireEvent.click(r.getByRole("button", { name: "Настройки" }));
    await fireEvent.click(r.getByRole("button", { name: "Дополнительно" }));
    await fireEvent.click(await r.findByRole("button", { name: "Сменить ключ" }));
    // Сам ключ меняет только подтверждение в модалке.
    expect(apiMock.rotateWdttServerDiagKey).not.toHaveBeenCalled();
    await fireEvent.click(
      within(await r.findByRole("dialog", { name: "Сменить ключ" })).getByRole("button", { name: "Сменить ключ" }),
    );

    await waitFor(() =>
      expect(apiMock.rotateWdttServerDiagKey).toHaveBeenCalledWith("default"),
    );
    await waitFor(() => expect(onrestart).toHaveBeenCalledTimes(1));
    expect(notify.success).toHaveBeenCalledWith(
      "Ключ диагностики обновлён",
    );
  });

  it("на остановленном сервере смена ключа не вызывает restart", async () => {
    const onrestart = vi.fn().mockResolvedValue(undefined);
    const r = render(ShareDetail, {
      props: {
        row,
        wdttServer,
        onstart: () => {},
        onstop: () => {},
        onrestart,
        onsave: async () => null,
        onreload: () => {},
      },
    });
    await waitFor(() =>
      expect(apiMock.singboxRouterGetSettings).toHaveBeenCalled(),
    );
    await fireEvent.click(r.getByRole("button", { name: "Настройки" }));
    await fireEvent.click(r.getByRole("button", { name: "Дополнительно" }));
    await fireEvent.click(await r.findByRole("button", { name: "Сменить ключ" }));
    // Сам ключ меняет только подтверждение в модалке.
    expect(apiMock.rotateWdttServerDiagKey).not.toHaveBeenCalled();
    await fireEvent.click(
      within(await r.findByRole("dialog", { name: "Сменить ключ" })).getByRole("button", { name: "Сменить ключ" }),
    );

    await waitFor(() =>
      expect(apiMock.rotateWdttServerDiagKey).toHaveBeenCalledWith("default"),
    );
    expect(onrestart).not.toHaveBeenCalled();
    expect(notify.success).toHaveBeenCalledWith("Ключ диагностики обновлён");
  });

  it("чужая мутация блокирует «Сменить ключ», но не включает её спиннер загрузки", async () => {
    let resolveSecond!: (v: ReturnType<typeof settings>) => void;
    apiMock.singboxRouterGetSettings
      .mockResolvedValueOnce(settings())
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveSecond = resolve;
          }),
      );

    const r = render(ShareDetail, {
      props: {
        row,
        wdttServer,
        onstart: () => {},
        onstop: () => {},
        onrestart: async () => {},
        onsave: async () => null,
        onreload: () => {},
      },
    });
    await waitFor(() =>
      expect(apiMock.singboxRouterGetSettings).toHaveBeenCalledTimes(1),
    );
    await fireEvent.click(r.getByRole("button", { name: "Настройки" }));
    await fireEvent.click(r.getByRole("button", { name: "Дополнительно" }));

    const toggle = r.getByRole("checkbox", { name: "Маршрутизация через sing-box" });
    await fireEvent.click(toggle);

    const btn = await r.findByRole("button", { name: "Сменить ключ" });
    await waitFor(() => expect(btn.hasAttribute("disabled")).toBe(true));
    expect(btn.getAttribute("aria-busy")).toBe("false");

    resolveSecond(settings());
    await waitFor(() => expect(btn.hasAttribute("disabled")).toBe(false));
  });
});
