// Режим NAT «Интернет» требует выбранного WAN (WdttServerConfig.Validate).
// Поля WAN в UI не было вовсе: кнопка «Интернет» была активна, конфиг после
// неё становился невалидным, сервер не стартовал — и починить было негде.
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

import { render, fireEvent } from "@testing-library/svelte";
import ShareNetworkSection from "./ShareNetworkSection.svelte";

function server(over: Partial<WdttServerConfig> = {}): WdttServerConfig {
  return {
    listen: "0.0.0.0:56002",
    wgPort: 56001,
    password: "",
    passwordSet: true,
    natMode: "full",
    relayMode: "wg",
    openFirewall: true,
    ...over,
  };
}

function mount(cfg: WdttServerConfig) {
  const onnat = vi.fn();
  const onsave = vi.fn();
  const r = render(ShareNetworkSection, {
    props: {
      wdttServer: cfg,
      lanOptions: [],
      onnat,
      onlan: () => {},
      onpolicy: () => {},
      onsave,
      onrevert: () => {},
    },
  });
  return { ...r, onnat, onsave };
}

describe("ShareNetworkSection: NAT «Интернет» без выбранного WAN", () => {
  beforeEach(() => vi.clearAllMocks());

  it("не уезжает на бэкенд: смена NAT идёт своей ручкой, мимо «Сохранить»", async () => {
    const { getByRole, onnat, findByText } = mount(server());
    await fireEvent.click(getByRole("button", { name: "Интернет" }));
    expect(onnat).not.toHaveBeenCalled();
    expect(await findByText(/Сначала выберите выход в интернет/)).toBeTruthy();
  });

  it("с выбранным WAN уезжает как обычно", async () => {
    const { getByRole, onnat } = mount(server({ natStaticWan: "ISP" }));
    await fireEvent.click(getByRole("button", { name: "Интернет" }));
    expect(onnat).toHaveBeenCalledWith("internet-only");
  });

  it("другие режимы не трогаем", async () => {
    const { getByRole, onnat } = mount(server());
    await fireEvent.click(getByRole("button", { name: "Без NAT" }));
    expect(onnat).toHaveBeenCalledWith("none");
  });

  it("сохранение заперто, если WAN стёрли при активном «Интернете»", () => {
    // Вторая дыра того же режима: WAN правится в черновике и уезжает через
    // «Сохранить» — одного перехвата смены NAT мало.
    const { getByRole } = mount(
      server({ natMode: "internet-only", natStaticWan: "" }),
    );
    expect(
      getByRole("button", { name: "Сохранить" }).hasAttribute("disabled"),
    ).toBe(true);
  });

  it("с заполненным WAN сохранение открыто", () => {
    const { getByRole } = mount(
      server({ natMode: "internet-only", natStaticWan: "ISP" }),
    );
    expect(
      getByRole("button", { name: "Сохранить" }).hasAttribute("disabled"),
    ).toBe(false);
  });

  it("переключатель режима работы WG/Raw есть в секции", () => {
    const { getByRole } = mount(server());
    const group = getByRole("group", { name: "Режим работы" });
    expect(group.textContent).toContain("WG");
    expect(group.textContent).toContain("Raw");
  });
});
