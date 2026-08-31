/**
 * Сквозные сценарии страницы «Прокси» (решение владельца 2026-08-27, ia.md §10).
 *
 * Их завели после прогона на живом роутере: двенадцать замечаний владельца не
 * поймал ни один из 1800+ тестов, потому что все они проверяли соответствие
 * документу, а дыры были В САМОМ документе. Эти три идут путями живого
 * человека, а не структурой кода.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";

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
  Object.defineProperty(globalThis, "scrollTo", {
    writable: true,
    value: () => {},
  });
});

// Tabs читает адрес страницы и правит его через goto — в тесте ни того, ни
// другого нет.
class ResizeObserverStub {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
vi.stubGlobal("ResizeObserver", ResizeObserverStub);
vi.mock("$app/navigation", () => ({ goto: vi.fn() }));
vi.mock("$app/stores", async () => {
  const { readable } = await import("svelte/store");
  return {
    page: readable({ url: new URL("http://localhost/proxy"), params: {} }),
  };
});

const emptyProc = { running: false, binary: "", binaryPresent: false };

/** Статус подсистемы в форме, которую реально отдаёт shim (proxyInstances.ts). */
function subsystemStatus(over: Record<string, unknown> = {}) {
  return {
    clients: [],
    servers: [],
    client: emptyProc,
    server: emptyProc,
    binariesPresent: true,
    installAvailable: true,
    installedVersion: "1.4.4-awgm+server-1.4.0-3",
    updateAvailable: false,
    installing: false,
    serverSupported: true,
    ...over,
  };
}

const api = vi.hoisted(() => {
  const fn = () => vi.fn();
  return {
    getWdttStatus: fn(),
    getFreeTurnStatus: fn(),
    getProxySeed: fn(),
    getWdttConfig: fn(),
    getFreeTurnConfig: fn(),
    listAccessPolicies: fn(),
    getTunnelsAll: fn(),
    listManagedLANSegments: fn(),
    singboxRouterGetSettings: fn(),
    singboxGetStatus: fn(),
    getWANInterfaces: fn(),
    getWdttServerPanelUsers: fn(),
    addWdttServerPanelUser: fn(),
    createWdttServer: fn(),
    updateWdttServerInstance: fn(),
    startWdttServerInstance: fn(),
    generateWdttServerLink: fn(),
    deleteWdttServer: fn(),
    deleteWdttClient: fn(),
    clearWdttLinkedTunnels: fn(),
    ackProxyListenMoves: fn(),
  };
});
vi.mock("$lib/api/client", () => ({ api }));

const notify = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  info: vi.fn(),
}));
vi.mock("$lib/stores/notifications", () => ({ notifications: notify }));

import { render, waitFor, fireEvent } from "@testing-library/svelte";
import ProxyPage from "../../../routes/proxy/+page.svelte";

/** Клиент как его отдаёт бэкенд (форма shim'а, все поля на месте). */
function wdttClientCfg(over: Record<string, unknown> = {}) {
  return {
    listen: "127.0.0.1:9000",
    peer: "vps.example:56002",
    password: "",
    vkHashes: "h1",
    workers: 9,
    obfs: "",
    fingerprint: "",
    captchaMode: "rjs",
    connMode: "wg",
    enabled: false,
    ...over,
  };
}

/** Сервер как его отдаёт бэкенд: секрет наружу не уходит, приходит признак. */
function wdttServerCfg(over: Record<string, unknown> = {}) {
  return {
    listen: "0.0.0.0:56002",
    wgPort: 56001,
    password: "",
    natMode: "full",
    relayMode: "wg",
    openFirewall: true,
    ...over,
  };
}

function setDefaults(
  over: { wdtt?: unknown; ft?: unknown; wdttCfg?: unknown } = {},
) {
  api.getWdttStatus.mockResolvedValue(over.wdtt ?? subsystemStatus());
  api.getFreeTurnStatus.mockResolvedValue(over.ft ?? subsystemStatus());
  api.getProxySeed.mockResolvedValue({ seeded: true, certified: true });
  api.getWdttConfig.mockResolvedValue(
    over.wdttCfg ?? { clients: [], servers: [] },
  );
  api.getFreeTurnConfig.mockResolvedValue({ clients: [], servers: [] });
  api.listAccessPolicies.mockResolvedValue([]);
  api.getTunnelsAll.mockResolvedValue({ tunnels: [] });
  api.listManagedLANSegments.mockResolvedValue([]);
  api.singboxRouterGetSettings.mockResolvedValue({ ingressInterfaces: [] });
  api.singboxGetStatus.mockResolvedValue({ running: false });
  api.getWANInterfaces.mockResolvedValue([
    { name: "ISP", label: "Провайдер", state: "up" },
  ]);
}

describe("Сценарий 1: пустая страница → раздача с абонентом → ссылка", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setDefaults();
    api.createWdttServer.mockResolvedValue({
      id: "default",
      config: wdttServerCfg(),
    });
    api.getWdttServerPanelUsers.mockResolvedValue({ users: [] });
    api.addWdttServerPanelUser.mockResolvedValue({
      users: [{ password: "abonent-pass", comment: "Телефон" }],
    });
    api.updateWdttServerInstance.mockImplementation(
      async (_id: string, cfg: unknown) => ({
        config: cfg,
      }),
    );
    api.startWdttServerInstance.mockResolvedValue(undefined);
    api.generateWdttServerLink.mockResolvedValue({
      link: "wdtt://готовая-ссылка",
    });
  });

  it("мастер доводит до ссылки, а сервер получает пароль и стартует", async () => {
    const { findByRole, getByRole, getByLabelText, findByLabelText } =
      render(ProxyPage);

    // Пустая раздача: единственный вход — кнопка списка.
    await fireEvent.click(await findByRole("button", { name: "Раздача" }));
    await fireEvent.click(
      await findByRole("button", { name: "Настроить раздачу" }),
    );

    // Шаг 1 — протокол (у НОВОГО инстанса он спрашивается).
    await fireEvent.click(await findByRole("button", { name: /^WDTT/ }));
    await fireEvent.click(getByRole("button", { name: "Дальше" }));

    // Шаг 2 — параметры сервера: пароля владельца шаг не спрашивает, порт
    // подставлен по умолчанию.
    await waitFor(() =>
      expect(
        getByRole("button", { name: "Дальше" }).hasAttribute("disabled"),
      ).toBe(false),
    );
    await fireEvent.click(getByRole("button", { name: "Дальше" }));

    // Шаг 3 — первый абонент. VK-хеш обязателен: без него ссылка не работает.
    await fireEvent.input(getByLabelText("Имя абонента"), {
      target: { value: "Телефон" },
    });
    await fireEvent.input(getByLabelText("VK-хеш"), {
      target: { value: "vk-hash-1" },
    });
    await fireEvent.click(getByRole("button", { name: "Дальше" }));

    // Шаг 4 — запуск и выдача ссылки.
    await fireEvent.click(
      getByRole("button", { name: "Запустить и выдать ссылку" }),
    );

    await waitFor(() =>
      expect(api.startWdttServerInstance).toHaveBeenCalledWith("default"),
    );

    // Работоспособным сервер делает абонент: форк роняет старт только когда
    // паролей нет ВОВСЕ, а пароля владельца мастер больше не заводит.
    expect(api.addWdttServerPanelUser).toHaveBeenCalled();
    const [, savedCfg] = api.updateWdttServerInstance.mock.calls.at(-1) as [
      string,
      { password?: string },
    ];
    expect(savedCfg.password ?? "").toBe("");
    expect(api.generateWdttServerLink).toHaveBeenCalled();
  });
});

describe("Сценарий 2: инстанс без обязательного поля чинится, не покидая детали", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Ровно состояние стенда: сервер заведён, пароля нет, выключен.
    setDefaults({
      wdtt: subsystemStatus({
        servers: [
          {
            id: "default",
            name: "Сервер",
            status: { ...emptyProc, binaryPresent: true },
          },
        ],
      }),
      wdttCfg: {
        clients: [],
        servers: [
          {
            id: "default",
            name: "Сервер",
            seededFrom: "wdtt.json",
            config: wdttServerCfg({ passwordSet: false }),
          },
        ],
      },
    });
    api.updateWdttServerInstance.mockImplementation(
      async (_id: string, cfg: unknown) => ({
        config: cfg,
      }),
    );
  });

  it("клик по инстансу открывает параметры, обязательное поле правится тут же", async () => {
    // Обязательное поле сервера — порт раздачи (`Validate`: «не задан listen
    // сервера»). Раньше он правился только в мастере, как и пароль.
    const { findByRole, getByRole, findByLabelText } = render(ProxyPage);
    await fireEvent.click(await findByRole("button", { name: "Раздача" }));

    // Деталь, а не мастер: заголовок — имя инстанса.
    expect(
      await findByRole("heading", { name: "Сервер", level: 2 }),
    ).toBeTruthy();

    // Настройки — в выдвижном ящике; обязательный порт лежит в основной его
    // части, а не в «экспертном» разделе.
    await fireEvent.click(getByRole("button", { name: "Настройки" }));
    const port = await findByLabelText("Порт раздачи");
    await fireEvent.change(port, { target: { value: "56010" } });
    await fireEvent.click(getByRole("button", { name: "Сохранить" }));

    await waitFor(() =>
      expect(api.updateWdttServerInstance).toHaveBeenCalled(),
    );
    const [, cfg] = api.updateWdttServerInstance.mock.calls.at(-1) as [
      string,
      { listen: string },
    ];
    expect(cfg.listen).toBe("0.0.0.0:56010");
  });

  it("перенесённый инстанс объясняет своё происхождение", async () => {
    const { findByRole } = render(ProxyPage);
    await fireEvent.click(await findByRole("button", { name: "Раздача" }));
    await findByRole("heading", { name: "Сервер", level: 2 });
    expect(
      await findByRole("button", { name: "Подсказка: перенесённые настройки" }),
    ).toBeTruthy();
  });
});

describe("Сценарий 3: удаление последнего инстанса не превращает продукт в «не установлен»", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setDefaults({
      wdtt: subsystemStatus({
        clients: [
          {
            id: "c1",
            name: "Выход",
            status: { ...emptyProc, binaryPresent: true },
          },
        ],
      }),
      wdttCfg: {
        clients: [{ id: "c1", name: "Выход", config: wdttClientCfg() }],
        servers: [],
      },
    });
    api.deleteWdttClient.mockResolvedValue({ deletedTunnels: [] });
  });

  it("после удаления полоса молчит: бинари на месте", async () => {
    const { findByRole, queryByText, getByRole } = render(ProxyPage);
    await findByRole("button", { name: /^Выход WDTT/ });

    // Инстансов больше нет — ровно состояние, в котором полоса врала.
    setDefaults({
      wdtt: subsystemStatus(),
      wdttCfg: { clients: [], servers: [] },
    });
    await fireEvent.click(
      getByRole("button", { name: "Удалить инстанс «Выход»?" }),
    );
    await fireEvent.click(await findByRole("button", { name: "Удалить" }));

    await waitFor(() =>
      expect(api.deleteWdttClient).toHaveBeenCalledWith("c1"),
    );
    await waitFor(() => expect(queryByText("не установлен")).toBeNull());
  });
});
