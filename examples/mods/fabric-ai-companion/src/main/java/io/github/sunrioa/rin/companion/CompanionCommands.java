package io.github.sunrioa.rin.companion;

import com.mojang.brigadier.arguments.StringArgumentType;
import com.mojang.brigadier.context.CommandContext;
import net.fabricmc.fabric.api.command.v2.CommandRegistrationCallback;
import net.minecraft.commands.CommandSourceStack;
import net.minecraft.commands.Commands;
import net.minecraft.network.chat.Component;
import net.minecraft.server.level.ServerPlayer;
import net.minecraft.server.permissions.PermissionSet;
import net.minecraft.server.permissions.Permissions;

final class CompanionCommands {
    private CompanionCommands() {
    }

    static void register() {
        CommandRegistrationCallback.EVENT.register((dispatcher, context, selection) -> dispatcher.register(
                Commands.literal("companion")
                        .then(Commands.literal("spawn").executes(CompanionCommands::spawn))
                        .then(Commands.literal("recall").executes(CompanionCommands::recall))
                        .then(Commands.literal("pause").executes(command -> mode(command, CompanionEntity.Mode.STOPPED)))
                        .then(Commands.literal("resume").executes(command -> mode(command, CompanionEntity.Mode.FOLLOW)))
                        .then(Commands.literal("status").executes(CompanionCommands::status))
                        .then(Commands.literal("skin").then(Commands.argument("player", StringArgumentType.word())
                                .executes(CompanionCommands::skin)))));
    }

    static boolean canConfigure(PermissionSet permissions) {
        return permissions.hasPermission(Permissions.COMMANDS_OWNER);
    }

    private static int spawn(CommandContext<CommandSourceStack> context) throws com.mojang.brigadier.exceptions.CommandSyntaxException {
        ServerPlayer player = context.getSource().getPlayerOrException();
        boolean created = runtime(player).spawnFor(player.getUUID(), player.level(), player.position());
        reply(context, created, "rin_ai_companion.command.spawned", "rin_ai_companion.command.exists");
        return created ? 1 : 0;
    }

    private static int recall(CommandContext<CommandSourceStack> context) throws com.mojang.brigadier.exceptions.CommandSyntaxException {
        ServerPlayer player = context.getSource().getPlayerOrException();
        boolean recalled = runtime(player).recall(player.getUUID(), player.level(), player.position());
        reply(context, recalled, "rin_ai_companion.command.recalled", "rin_ai_companion.command.missing");
        return recalled ? 1 : 0;
    }

    private static int mode(CommandContext<CommandSourceStack> context, CompanionEntity.Mode mode) throws com.mojang.brigadier.exceptions.CommandSyntaxException {
        ServerPlayer player = context.getSource().getPlayerOrException();
        boolean changed = runtime(player).setMode(player.getUUID(), mode);
        reply(context, changed, "rin_ai_companion.command.mode", "rin_ai_companion.command.missing");
        return changed ? 1 : 0;
    }

    private static int status(CommandContext<CommandSourceStack> context) throws com.mojang.brigadier.exceptions.CommandSyntaxException {
        ServerPlayer player = context.getSource().getPlayerOrException();
        String status = runtime(player).status(player.getUUID());
        context.getSource().sendSuccess(() -> Component.translatable("rin_ai_companion.command.status", status), false);
        return 1;
    }

    private static int skin(CommandContext<CommandSourceStack> context) throws com.mojang.brigadier.exceptions.CommandSyntaxException {
        ServerPlayer player = context.getSource().getPlayerOrException();
        boolean changed = runtime(player).setSkin(player.getUUID(), StringArgumentType.getString(context, "player"));
        reply(context, changed, "rin_ai_companion.command.skin", "rin_ai_companion.command.skin_invalid");
        return changed ? 1 : 0;
    }

    private static CompanionRuntime runtime(ServerPlayer player) {
        return CompanionRuntime.forServer(player.level().getServer());
    }

    private static void reply(CommandContext<CommandSourceStack> context, boolean success, String ok, String failure) {
        if (success) context.getSource().sendSuccess(() -> Component.translatable(ok), false);
        else context.getSource().sendFailure(Component.translatable(failure));
    }
}
